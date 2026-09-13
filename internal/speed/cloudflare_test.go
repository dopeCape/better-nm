package speed

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// fakeCF emulates speed.cloudflare.com's __down and __up.
type fakeCF struct {
	srv          *httptest.Server
	latencyDelay time.Duration // sleep before answering __down?bytes=0
	serverTiming string
	rateLimit    atomic.Bool // answer 429 to everything
	mu           sync.Mutex
	paths        []string
	agents       []string
	downBytes    int64
	upBytes      int64
	maxDown      int64
	maxUp        int64
}

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{serverTiming: "cfSpeedEdge;dur=2, cfSpeedWorker;dur=18"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCF) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.agents = append(f.agents, r.UserAgent())
	f.mu.Unlock()
	if f.rateLimit.Load() {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	w.Header().Add("Server-Timing", `cfL4;desc="?proto=TCP&rtt=16451"`)
	w.Header().Set("cf-meta-colo", "BOM")
	// Loopback is too fast for the engine's 10 ms minimum, so transfers are
	// throttled to ~64 KB/ms in both directions.
	const chunkSize, chunkDelay = 64 << 10, time.Millisecond
	switch r.URL.Path {
	case "/__down":
		n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if n == 0 {
			time.Sleep(f.latencyDelay)
			w.Header().Set("Server-Timing", f.serverTiming)
		} else {
			w.Header().Set("Server-Timing", "cfSpeedEdge;dur=1")
		}
		f.mu.Lock()
		f.downBytes += n
		if n > f.maxDown {
			f.maxDown = n
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, chunkSize)
		fl, _ := w.(http.Flusher)
		for n > 0 {
			c := int64(len(chunk))
			if n < c {
				c = n
			}
			if _, err := w.Write(chunk[:c]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(chunkDelay)
			n -= c
		}
	case "/__up":
		w.Header().Set("Server-Timing", "cfSpeedEdge;dur=1")
		var n int64
		buf := make([]byte, chunkSize)
		for {
			k, err := io.ReadFull(r.Body, buf)
			n += int64(k)
			if err != nil {
				break
			}
			time.Sleep(chunkDelay)
		}
		f.mu.Lock()
		f.upBytes += n
		if n > f.maxUp {
			f.maxUp = n
		}
		f.mu.Unlock()
		w.Header().Set("cf-meta-upload-bytes", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeCF) hit(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == path {
			n++
		}
	}
	return n
}

func TestServerTimingParse(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want time.Duration
	}{
		{"none", nil, 0},
		{"cfSpeed sum", []string{"cfSpeedEdge;dur=2, cfSpeedWorker;dur=18"}, 20 * time.Millisecond},
		{"cfRequestDuration wins", []string{"cfSpeedEdge;dur=2", "cfRequestDuration;dur=7.5", "cfSpeedWorker;dur=100"}, 7500 * time.Microsecond},
		{"ignores others", []string{`cfL4;desc="?proto=TCP&rtt=16451", cache;desc=HIT;dur=3`}, 0},
		{"quoted and spaced", []string{` cfSpeedEdge ; dur="4" `}, 4 * time.Millisecond},
		{"garbage", []string{"cfSpeedEdge;dur=abc"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := serverTiming(tc.in); got != tc.want {
				t.Errorf("serverTiming(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMedianAndJitter(t *testing.T) {
	if m := median(nil); m != 0 {
		t.Errorf("median(nil) = %v", m)
	}
	if m := median([]float64{5, 1, 3}); m != 3 {
		t.Errorf("median = %v", m)
	}
	if m := median([]float64{4, 1, 3, 2}); m != 2.5 {
		t.Errorf("median even = %v", m)
	}
	if j := jitter([]float64{10}); j != 0 {
		t.Errorf("jitter single = %v", j)
	}
	if j := jitter([]float64{10, 12, 9, 13}); j != (2+3+4)/3.0 {
		t.Errorf("jitter = %v", j)
	}
}

func TestUserAgent(t *testing.T) {
	ua := UserAgent()
	if !strings.HasPrefix(ua, "bnm/") || !strings.Contains(ua, "(+https://github.com/dopeCape/better-nm)") {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestQuickRunAgainstFake(t *testing.T) {
	f := newFakeCF(t)
	f.latencyDelay = 30 * time.Millisecond
	c := New(ProviderCloudflare, WithBaseURL(f.srv.URL), WithHTTPClient(f.srv.Client())).(*Cloudflare)
	c.LatencySamples = 4

	var mu sync.Mutex
	phases := map[string]int{}
	var lastPercent float64
	res, err := c.Run(context.Background(), core.SpeedOptions{Quick: true, NetworkKey: "wifi:Home"}, func(p core.SpeedProgress) {
		mu.Lock()
		defer mu.Unlock()
		phases[p.Phase]++
		if p.Percent > lastPercent {
			lastPercent = p.Percent
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Latency: ~30 ms TTFB minus 20 ms of server-timing -> ~10 ms.
	if res.LatencyMs < 5 || res.LatencyMs > 25 {
		t.Errorf("latency = %.1f ms, want ~10 (30 ms delay minus 20 ms server time)", res.LatencyMs)
	}
	if res.JitterMs < 0 || res.JitterMs > 20 {
		t.Errorf("jitter = %.1f ms", res.JitterMs)
	}
	if res.Server != "BOM" || res.Provider != ProviderCloudflare || !res.Quick || res.NetworkKey != "wifi:Home" {
		t.Errorf("result meta = %+v", res)
	}
	if res.DownloadMbps <= 0 || res.UploadMbps <= 0 {
		t.Errorf("throughput not measured: down=%.1f up=%.1f", res.DownloadMbps, res.UploadMbps)
	}
	if res.BytesMoved <= 0 || res.BytesMoved > QuickMaxBytes {
		t.Errorf("BytesMoved = %d, want within the quick budget %d", res.BytesMoved, QuickMaxBytes)
	}
	if res.Duration <= 0 {
		t.Error("Duration not set")
	}
	f.mu.Lock()
	maxDown, maxUp := f.maxDown, f.maxUp
	f.mu.Unlock()
	if maxDown > quickDownMax {
		t.Errorf("quick mode downloaded a %d-byte request, cap is %d", maxDown, quickDownMax)
	}
	if maxUp > quickUpMax {
		t.Errorf("quick mode uploaded a %d-byte request, cap is %d", maxUp, quickUpMax)
	}
	if f.hit("/__results") != 0 {
		t.Error("posted to __results")
	}
	if f.hit("/__down") < 5 || f.hit("/__up") == 0 {
		t.Errorf("hits: down=%d up=%d", f.hit("/__down"), f.hit("/__up"))
	}
	for _, ua := range f.agents {
		if ua != UserAgent() {
			t.Errorf("User-Agent sent = %q", ua)
		}
	}
	for _, phase := range []string{"latency", "download", "upload", "done"} {
		if phases[phase] == 0 {
			t.Errorf("no progress for phase %s", phase)
		}
	}
	if lastPercent != 100 {
		t.Errorf("progress never reached 100: %v", lastPercent)
	}
}

func TestMaxBytesIsEnforced(t *testing.T) {
	f := newFakeCF(t)
	c := &Cloudflare{BaseURL: f.srv.URL, Client: f.srv.Client(), LatencySamples: 2}
	const cap = 2 << 20
	res, err := c.Run(context.Background(), core.SpeedOptions{MaxBytes: cap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.BytesMoved > cap {
		t.Errorf("BytesMoved = %d > MaxBytes %d", res.BytesMoved, cap)
	}
	f.mu.Lock()
	moved := f.downBytes + f.upBytes
	f.mu.Unlock()
	if moved > cap {
		t.Errorf("server saw %d bytes > cap %d", moved, cap)
	}
	if res.DownloadMbps <= 0 {
		t.Error("download should still have a value from the small stages")
	}
}

func TestDownloadMathAgainstThrottledServer(t *testing.T) {
	// The server takes ~100 ms per MB: the measured throughput must land
	// near 80 Mbit/s, not at loopback speed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		w.Header().Set("Server-Timing", "cfSpeedEdge;dur=0")
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusOK)
		if n == 0 {
			return
		}
		chunk := make([]byte, 100<<10) // 100 KB every 10 ms = 10 MB/s = 80 Mbit/s
		fl, _ := w.(http.Flusher)
		for n > 0 {
			c := int64(len(chunk))
			if n < c {
				c = n
			}
			if _, err := w.Write(chunk[:c]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			n -= c
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()
	c := &Cloudflare{BaseURL: srv.URL, Client: srv.Client(), LatencySamples: 1}
	b := &budget{max: 4 << 20}
	mbps, err := c.ramp(context.Background(), "download", []stage{{1 << 20, 2}}, false, 0, b, func(core.SpeedProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	if mbps < 40 || mbps > 120 {
		t.Errorf("download = %.1f Mbit/s, want ~80 from a throttled server", mbps)
	}
	if b.used != 2<<20 {
		t.Errorf("budget used = %d", b.used)
	}
}

func TestRateLimitIsSoftError(t *testing.T) {
	f := newFakeCF(t)
	f.rateLimit.Store(true)
	c := &Cloudflare{BaseURL: f.srv.URL, Client: f.srv.Client(), LatencySamples: 2}
	res, err := c.Run(context.Background(), core.SpeedOptions{Quick: true}, nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second || rl.Phase != "latency" {
		t.Errorf("RateLimitedError = %+v", rl)
	}
	if core.KindOf(err) != core.KindUnavailable || !strings.Contains(core.HintOf(err), "30s") {
		t.Errorf("kind = %s hint = %q, want unavailable with the retry-after", core.KindOf(err), core.HintOf(err))
	}
	if res.Provider != ProviderCloudflare || res.Duration <= 0 {
		t.Errorf("partial result = %+v", res)
	}
}

func TestRateLimitMidDownloadKeepsLatency(t *testing.T) {
	f := newFakeCF(t)
	f.latencyDelay = 30 * time.Millisecond
	c := &Cloudflare{BaseURL: f.srv.URL, Client: f.srv.Client(), LatencySamples: 2}
	// Flip to 429 after the latency phase.
	res, err := c.Run(context.Background(), core.SpeedOptions{Quick: true}, func(p core.SpeedProgress) {
		if p.Phase == "download" && p.Percent == 0 {
			f.rateLimit.Store(true)
		}
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if res.LatencyMs <= 0 {
		t.Errorf("latency from before the 429 was lost: %+v", res)
	}
	if res.DownloadMbps != 0 {
		t.Errorf("download = %v, want 0 after 429 at the first request", res.DownloadMbps)
	}
}

func TestContextCancelStopsRun(t *testing.T) {
	f := newFakeCF(t)
	f.latencyDelay = 200 * time.Millisecond
	c := &Cloudflare{BaseURL: f.srv.URL, Client: f.srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Run(ctx, core.SpeedOptions{Quick: true}, nil)
	if err == nil {
		t.Fatal("expected an error after context timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Run took %v after cancel", time.Since(start))
	}
}

func TestServerErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := &Cloudflare{BaseURL: srv.URL, Client: srv.Client()}
	if _, err := c.Run(context.Background(), core.SpeedOptions{}, nil); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want HTTP 502 mention", err)
	}
}

func TestFactory(t *testing.T) {
	if _, ok := New("").(*Cloudflare); !ok {
		t.Error("empty provider should be cloudflare")
	}
	if _, ok := New(ProviderCloudflare).(*Cloudflare); !ok {
		t.Error("cloudflare provider")
	}
	if p, ok := New(ProviderIperf3, WithIperf3Binary("/x/iperf3")).(*Iperf3); !ok || p.Binary != "/x/iperf3" {
		t.Error("iperf3 provider or binary option")
	}
	_, err := New("ookla").Run(context.Background(), core.SpeedOptions{}, nil)
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown provider err = %v", err)
	}
	if _, err := New(ProviderCloudflare, WithBaseURL("::not a url")).Run(context.Background(), core.SpeedOptions{}, nil); err == nil {
		t.Error("bad base URL should fail at Run")
	}
	if c := New(ProviderCloudflare, WithBaseURL("https://mirror.example/")).(*Cloudflare); c.BaseURL != "https://mirror.example" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
}

func TestDefaultBudget(t *testing.T) {
	for _, tc := range []struct {
		opts core.SpeedOptions
		want int64
	}{
		{core.SpeedOptions{}, DefaultMaxBytes},
		{core.SpeedOptions{Quick: true}, QuickMaxBytes},
		{core.SpeedOptions{Quick: true, MaxBytes: 5 << 20}, 5 << 20},
		{core.SpeedOptions{MaxBytes: 1}, 1},
	} {
		if got := maxBytesFor(tc.opts); got != tc.want {
			t.Errorf("maxBytesFor(%+v) = %d, want %d", tc.opts, got, tc.want)
		}
	}
	b := &budget{max: 10}
	if !b.allows(10) || b.allows(11) {
		t.Error("budget bounds wrong")
	}
	b.add(4)
	if b.allows(7) || !b.allows(6) {
		t.Error("budget after add wrong")
	}
}
