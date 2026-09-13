// Package speed runs on-demand bandwidth tests (core.SpeedTester). Providers:
// Cloudflare (speed.cloudflare.com __down/__up, native net/http, the default)
// and iperf3 (shells out to the distro binary against a host the user names).
// The daemon never runs these in the background.
//
// Tests: the Cloudflare maths against an httptest server emulating
// __down/__up with a server-timing header; iperf3 parsing against canned JSON
// and a fake binary. One `//go:build live` test hits the real Cloudflare.
package speed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/version"
)

// Provider names accepted by New and core.SpeedOptions.Provider.
const (
	ProviderCloudflare = "cloudflare"
	ProviderIperf3     = "iperf3"
)

// Cloudflare defaults.
const (
	CloudflareBaseURL = "https://speed.cloudflare.com"
	DefaultMaxBytes   = 300 << 20 // 300 MB
	QuickMaxBytes     = 30 << 20  // 30 MB
	latencySamples    = 10
	// minRequestDuration: shorter transfers are discarded (engine default 10 ms).
	minRequestDuration = 10 * time.Millisecond
	// finishRequestDuration: once a request lasts this long, bigger sizes are skipped.
	finishRequestDuration = time.Second
)

// ErrRateLimited is returned (wrapped in *RateLimitedError) when Cloudflare
// answers 429: back off, do not retry in a loop.
var ErrRateLimited = errors.New("speed: rate limited by server")

// RateLimitedError carries the server's Retry-After.
type RateLimitedError struct {
	RetryAfter time.Duration
	Phase      string
}

func (e *RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("speed: rate limited during %s, retry after %s", e.Phase, e.RetryAfter)
	}
	return fmt.Sprintf("speed: rate limited during %s", e.Phase)
}

// Unwrap matches ErrRateLimited and core.ErrUnavailable (the API answers 503).
func (e *RateLimitedError) Unwrap() []error { return []error{ErrRateLimited, core.ErrUnavailable} }

// Hint tells the caller what to do (core.HintOf).
func (e *RateLimitedError) Hint() string {
	if e.RetryAfter > 0 {
		return "wait " + e.RetryAfter.String() + " before running another speed test"
	}
	return "wait a minute before running another speed test"
}

// stage is one rung of the download/upload ramp.
type stage struct {
	bytes int64
	count int
}

var (
	downloadRamp = []stage{{100 << 10, 4}, {1 << 20, 3}, {10 << 20, 2}, {25 << 20, 2}, {100 << 20, 1}}
	uploadRamp   = []stage{{100 << 10, 4}, {1 << 20, 3}, {10 << 20, 2}}
	quickDownMax = int64(10 << 20)
	quickUpMax   = int64(1 << 20)
)

// Cloudflare implements core.SpeedTester against speed.cloudflare.com.
type Cloudflare struct {
	BaseURL string       // default CloudflareBaseURL
	Client  *http.Client // default: a client with keep-alive and no timeout (ctx bounds it)
	// LatencySamples overrides the number of latency requests (default 10).
	LatencySamples int
}

var _ core.SpeedTester = (*Cloudflare)(nil)

// UserAgent identifies bnm to the server, as the research doc recommends.
func UserAgent() string {
	return "bnm/" + version.Version + " (+https://github.com/dopeCape/better-nm)"
}

func (c *Cloudflare) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return CloudflareBaseURL
}

func (c *Cloudflare) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func (c *Cloudflare) latencyCount() int {
	if c.LatencySamples > 0 {
		return c.LatencySamples
	}
	return latencySamples
}

// budget tracks the byte cap across phases.
type budget struct {
	max  int64
	used int64
}

func (b *budget) allows(n int64) bool { return b.used+n <= b.max }
func (b *budget) add(n int64)         { b.used += n }

// maxBytesFor is the byte cap: opts.MaxBytes, else 300 MB (quick: 30 MB).
func maxBytesFor(opts core.SpeedOptions) int64 {
	if opts.MaxBytes > 0 {
		return opts.MaxBytes
	}
	if opts.Quick {
		return QuickMaxBytes
	}
	return DefaultMaxBytes
}

// Run executes latency, download and upload phases and returns the result.
// A 429 stops the test; the partial result is returned with *RateLimitedError.
func (c *Cloudflare) Run(ctx context.Context, opts core.SpeedOptions, progress func(core.SpeedProgress)) (core.SpeedResult, error) {
	if progress == nil {
		progress = func(core.SpeedProgress) {}
	}
	start := time.Now()
	res := core.SpeedResult{Time: start, NetworkKey: opts.NetworkKey, Provider: ProviderCloudflare, Quick: opts.Quick}
	b := &budget{max: maxBytesFor(opts)}
	finish := func(err error) (core.SpeedResult, error) {
		res.BytesMoved = b.used
		res.Duration = time.Since(start)
		return res, err
	}

	// Phase 1: latency.
	progress(core.SpeedProgress{Phase: "latency"})
	lat, jit, colo, err := c.latency(ctx, progress)
	if err != nil {
		return finish(err)
	}
	res.LatencyMs, res.JitterMs, res.Server = lat, jit, colo
	progress(core.SpeedProgress{Phase: "latency", Percent: 100})

	// Phase 2: download.
	progress(core.SpeedProgress{Phase: "download"})
	down, err := c.ramp(ctx, "download", downloadRamp, opts.Quick, quickDownMax, b, progress)
	res.DownloadMbps = down
	if err != nil {
		return finish(err)
	}
	progress(core.SpeedProgress{Phase: "download", Mbps: down, Percent: 100, Bytes: b.used})

	// Phase 3: upload.
	progress(core.SpeedProgress{Phase: "upload"})
	up, err := c.ramp(ctx, "upload", uploadRamp, opts.Quick, quickUpMax, b, progress)
	res.UploadMbps = up
	if err != nil {
		return finish(err)
	}
	progress(core.SpeedProgress{Phase: "upload", Mbps: up, Percent: 100, Bytes: b.used})
	progress(core.SpeedProgress{Phase: "done", Percent: 100, Bytes: b.used})
	return finish(nil)
}

// timing is what one request measured.
type timing struct {
	ttfb       time.Duration // request written -> first response byte
	payload    time.Duration // first byte -> body fully read
	total      time.Duration // request start -> body fully read
	serverTime time.Duration // from server-timing
	status     int
	colo       string
	retryAfter time.Duration
	bytes      int64
}

// do performs one request against path with the given body (nil for GET).
func (c *Cloudflare) do(ctx context.Context, method, rawURL string, body []byte) (timing, error) {
	var (
		t       timing
		wrote   time.Time
		first   time.Time
		reqBody io.Reader
	)
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
	if err != nil {
		return t, fmt.Errorf("speed: build request: %w", err)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("User-Agent", UserAgent())
	req.Header.Set("Cache-Control", "no-store")
	start := time.Now()
	wrote = start
	trace := &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote = time.Now() },
		GotFirstResponseByte: func() { first = time.Now() },
	}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))
	resp, err := c.client().Do(req)
	if err != nil {
		return t, fmt.Errorf("speed: %s %s: %w", method, rawURL, err)
	}
	defer resp.Body.Close()
	if first.IsZero() {
		first = time.Now()
	}
	n, err := io.Copy(io.Discard, resp.Body)
	end := time.Now()
	if err != nil {
		return t, fmt.Errorf("speed: read %s: %w", rawURL, err)
	}
	t.status = resp.StatusCode
	t.colo = resp.Header.Get("cf-meta-colo")
	t.bytes = n
	t.ttfb = first.Sub(wrote)
	t.payload = end.Sub(first)
	t.total = end.Sub(start)
	t.serverTime = serverTiming(resp.Header.Values("Server-Timing"))
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			t.retryAfter = time.Duration(secs) * time.Second
		}
	}
	return t, nil
}

// serverTiming extracts the edge's processing time: cfRequestDuration if
// present, else the sum of every cfSpeed*;dur= entry. Values are ms.
func serverTiming(headers []string) time.Duration {
	var speedSum float64
	for _, h := range headers {
		for _, entry := range strings.Split(h, ",") {
			parts := strings.Split(strings.TrimSpace(entry), ";")
			if len(parts) == 0 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			var dur float64
			found := false
			for _, p := range parts[1:] {
				p = strings.TrimSpace(p)
				if v, ok := strings.CutPrefix(p, "dur="); ok {
					if f, err := strconv.ParseFloat(strings.Trim(v, `"`), 64); err == nil {
						dur, found = f, true
					}
				}
			}
			if !found {
				continue
			}
			if name == "cfRequestDuration" {
				return time.Duration(dur * float64(time.Millisecond))
			}
			if strings.HasPrefix(name, "cfSpeed") {
				speedSum += dur
			}
		}
	}
	return time.Duration(speedSum * float64(time.Millisecond))
}

func (c *Cloudflare) downURL(n int64) string {
	return c.base() + "/__down?bytes=" + strconv.FormatInt(n, 10)
}

func (c *Cloudflare) upURL() string { return c.base() + "/__up" }

// latency does N zero-byte GETs and returns median (ms), jitter (ms) and colo.
func (c *Cloudflare) latency(ctx context.Context, progress func(core.SpeedProgress)) (float64, float64, string, error) {
	n := c.latencyCount()
	// Warm the connection (TLS handshake) outside the measurement.
	if t, err := c.do(ctx, http.MethodGet, c.downURL(0), nil); err != nil {
		return 0, 0, "", err
	} else if t.status == http.StatusTooManyRequests {
		return 0, 0, "", &RateLimitedError{RetryAfter: t.retryAfter, Phase: "latency"}
	} else if t.status/100 != 2 {
		return 0, 0, "", fmt.Errorf("speed: latency: unexpected HTTP %d", t.status)
	}
	var samples []float64
	colo := ""
	for i := 0; i < n; i++ {
		t, err := c.do(ctx, http.MethodGet, c.downURL(0), nil)
		if err != nil {
			return 0, 0, "", err
		}
		if t.status == http.StatusTooManyRequests {
			return 0, 0, "", &RateLimitedError{RetryAfter: t.retryAfter, Phase: "latency"}
		}
		if t.status/100 != 2 {
			return 0, 0, "", fmt.Errorf("speed: latency: unexpected HTTP %d", t.status)
		}
		ms := float64(t.ttfb-t.serverTime) / float64(time.Millisecond)
		if ms < 0 {
			ms = 0
		}
		samples = append(samples, ms)
		if t.colo != "" {
			colo = t.colo
		}
		progress(core.SpeedProgress{Phase: "latency", Percent: 100 * float64(i+1) / float64(n)})
	}
	return median(samples), jitter(samples), colo, nil
}

// ramp walks the size ladder in one direction and returns the best Mbps seen.
func (c *Cloudflare) ramp(ctx context.Context, phase string, ramp []stage, quick bool, quickMax int64,
	b *budget, progress func(core.SpeedProgress)) (float64, error) {
	best := 0.0
	total := 0
	for _, s := range ramp {
		if !quick || s.bytes <= quickMax {
			total += s.count
		}
	}
	done := 0
	var payload []byte
	if phase == "upload" {
		payload = make([]byte, ramp[len(ramp)-1].bytes)
		for i := range payload { // not all zeros: avoid any transparent compression
			payload[i] = byte(i*31 + i>>8)
		}
	}
	for _, s := range ramp {
		if quick && s.bytes > quickMax {
			break
		}
		stop := false
		for i := 0; i < s.count; i++ {
			if !b.allows(s.bytes) {
				return best, nil
			}
			var (
				t   timing
				err error
			)
			if phase == "upload" {
				t, err = c.do(ctx, http.MethodPost, c.upURL(), payload[:s.bytes])
			} else {
				t, err = c.do(ctx, http.MethodGet, c.downURL(s.bytes), nil)
			}
			if err != nil {
				return best, err
			}
			b.add(s.bytes)
			done++
			if t.status == http.StatusTooManyRequests {
				return best, &RateLimitedError{RetryAfter: t.retryAfter, Phase: phase}
			}
			if t.status/100 != 2 {
				return best, fmt.Errorf("speed: %s: unexpected HTTP %d", phase, t.status)
			}
			var dur time.Duration
			if phase == "upload" {
				// Request start .. first response byte: covers writing the body.
				dur = t.total - t.payload
			} else {
				// Edge time can exceed a fast link's TTFB; never go negative.
				dur = max(t.ttfb-t.serverTime, 0) + t.payload
			}
			if dur >= minRequestDuration {
				mbps := 8 * float64(s.bytes) / dur.Seconds() / 1e6
				if mbps > best {
					best = mbps
				}
			}
			progress(core.SpeedProgress{Phase: phase, Mbps: best, Percent: 100 * float64(done) / float64(total), Bytes: b.used})
			if t.total >= finishRequestDuration {
				stop = true
			}
		}
		if stop {
			break
		}
	}
	return best, nil
}

// ---- maths ----------------------------------------------------------------

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// jitter is the mean absolute difference between consecutive samples.
func jitter(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	sum := 0.0
	for i := 1; i < len(v); i++ {
		sum += math.Abs(v[i] - v[i-1])
	}
	return sum / float64(len(v)-1)
}

// parseBase validates a user-supplied base URL.
func parseBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("speed: invalid base URL %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}
