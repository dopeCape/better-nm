package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/store"
)

// scriptedProber answers per address; rtts may be changed between rounds.
type scriptedProber struct {
	mu    sync.Mutex
	rtts  map[string]float64 // addr -> rtt; missing = 20
	loss  map[string]float64
	calls map[string]int
	err   error
}

func newScripted() *scriptedProber {
	return &scriptedProber{rtts: map[string]float64{}, loss: map[string]float64{}, calls: map[string]int{}}
}

func (s *scriptedProber) set(addr string, rtt float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rtts[addr] = rtt
}

func (s *scriptedProber) Probe(_ context.Context, addr string) (float64, float64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[addr]++
	if s.err != nil {
		return -1, 1, MethodICMP, s.err
	}
	rtt, ok := s.rtts[addr]
	if !ok {
		rtt = 20
	}
	return rtt, s.loss[addr], MethodICMP, nil
}

func (s *scriptedProber) count(addr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[addr]
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fakeDNSProbe(context.Context, string) (float64, error) { return 12, nil }

func newTestMonitor(t *testing.T, prober Prober, cfg Config) (*Monitor, *store.DB) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if cfg.Interval == 0 {
		cfg.Interval = 10 * time.Millisecond
	}
	if cfg.DNS == nil {
		cfg.DNS = fakeDNSProbe
	}
	return New(cfg, st, prober, quietLogger()), st
}

func waitChange(t *testing.T, m *Monitor, path string, timeout time.Duration) core.Change {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case c := <-m.Changes():
			if c.Kind != core.ChangeMonitor {
				t.Fatalf("change kind = %s", c.Kind)
			}
			if path == "" || c.Path == path {
				return c
			}
		case <-deadline:
			t.Fatalf("no %q change within %v", path, timeout)
		}
	}
}

func TestIdleWithoutNetwork(t *testing.T) {
	p := newScripted()
	m, st := newTestMonitor(t, p, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond)
	if n := p.count("1.1.1.1"); n != 0 {
		t.Errorf("probed %d times while idle", n)
	}
	if n, _ := st.SampleCount(ctx); n != 0 {
		t.Errorf("%d samples stored while idle", n)
	}
	s := m.Status()
	if s.State != core.BaselineIdle || s.NetworkKey != "" || s.Interval != 10*time.Millisecond || s.Paused {
		t.Errorf("status = %+v", s)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on ctx cancel")
	}
}

func TestRoundWritesThreeSamplesAndAChange(t *testing.T) {
	p := newScripted()
	p.set("192.168.1.1", 2)
	m, st := newTestMonitor(t, p, Config{Interval: time.Hour}) // only the SetNetwork-triggered round
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	m.SetNetwork("wifi:Home", "192.168.1.1")
	waitChange(t, m, "round", 2*time.Second)

	for _, anchor := range []string{GatewayAnchor, "1.1.1.1", "8.8.8.8"} {
		rows, err := st.Samples(ctx, "wifi:Home", anchor, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("anchor %s: %d rows, want 1", anchor, len(rows))
		}
		r := rows[0]
		if r.Method != MethodICMP || r.Loss != 0 || r.NetworkKey != "wifi:Home" {
			t.Errorf("anchor %s row = %+v", anchor, r)
		}
		switch anchor {
		case GatewayAnchor:
			if r.AnchorAddr != "192.168.1.1" || r.RTTms != 2 || r.DNSms != 12 {
				t.Errorf("gateway row = %+v (dns goes on the first row)", r)
			}
		default:
			if r.AnchorAddr != anchor || r.RTTms != 20 || r.DNSms != -1 {
				t.Errorf("%s row = %+v", anchor, r)
			}
		}
	}
	if n, _ := st.SampleCount(ctx); n != 3 {
		t.Errorf("SampleCount = %d, want 3", n)
	}

	s := m.Status()
	if s.NetworkKey != "wifi:Home" || s.State != core.BaselineLearning || len(s.Anchors) != 3 || s.LastSample.IsZero() {
		t.Errorf("status = %+v", s)
	}
	bl, err := st.Baselines(ctx, "wifi:Home")
	if err != nil || len(bl) != 3 || bl[0].Anchor != GatewayAnchor || bl[0].SampleCount != 1 {
		t.Errorf("stored baselines = %+v err=%v", bl, err)
	}
	if s.Anchors[0].CurrentDNS != 12 {
		t.Errorf("gateway CurrentDNS = %v, want 12", s.Anchors[0].CurrentDNS)
	}

	// Samples() with "" means the current network.
	rows, err := m.Samples(ctx, "", "1.1.1.1", 10)
	if err != nil || len(rows) != 1 {
		t.Errorf("Samples(current) = %d rows err=%v", len(rows), err)
	}
}

func TestNoGatewayProbesOnlyPublicAnchors(t *testing.T) {
	p := newScripted()
	m, st := newTestMonitor(t, p, Config{Interval: time.Hour, Anchors: []string{"9.9.9.9"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	m.SetNetwork("wired:abc", "")
	waitChange(t, m, "round", 2*time.Second)
	if n, _ := st.SampleCount(ctx); n != 1 {
		t.Errorf("SampleCount = %d, want 1", n)
	}
	rows, _ := st.Samples(ctx, "wired:abc", "9.9.9.9", 0)
	if len(rows) != 1 || rows[0].DNSms != 12 {
		t.Errorf("dns should land on the first (only) row: %+v", rows)
	}
}

func TestProbeErrorStoresLostSample(t *testing.T) {
	p := newScripted()
	p.err = errors.New("boom")
	m, st := newTestMonitor(t, p, Config{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	m.SetNetwork("wifi:Home", "192.168.1.1")
	waitChange(t, m, "round", 2*time.Second)
	rows, _ := st.Samples(ctx, "wifi:Home", "1.1.1.1", 0)
	if len(rows) != 1 || rows[0].RTTms != -1 || rows[0].Loss != 1 {
		t.Errorf("errored probe row = %+v, want rtt -1 loss 1", rows)
	}
}

func TestPauseResume(t *testing.T) {
	p := newScripted()
	m, _ := newTestMonitor(t, p, Config{Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	m.SetNetwork("wifi:Home", "192.168.1.1")
	waitChange(t, m, "round", 2*time.Second)

	m.Pause()
	waitChange(t, m, "paused", time.Second)
	// drain any round that was already in flight
	time.Sleep(30 * time.Millisecond)
	for len(m.Changes()) > 0 {
		<-m.Changes()
	}
	before := p.count("1.1.1.1")
	time.Sleep(50 * time.Millisecond)
	if after := p.count("1.1.1.1"); after != before {
		t.Errorf("probed while paused: %d -> %d", before, after)
	}
	if !m.Status().Paused {
		t.Error("Status().Paused false while paused")
	}

	m.Resume()
	waitChange(t, m, "resumed", time.Second)
	waitChange(t, m, "round", time.Second)
	if m.Status().Paused {
		t.Error("Status().Paused true after resume")
	}
}

func TestSwitchingNetworkGoesIdle(t *testing.T) {
	p := newScripted()
	m, _ := newTestMonitor(t, p, Config{Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	m.SetNetwork("wifi:Home", "192.168.1.1")
	waitChange(t, m, "round", 2*time.Second)
	m.SetNetwork("", "")
	waitChange(t, m, "network", time.Second)
	time.Sleep(30 * time.Millisecond)
	for len(m.Changes()) > 0 {
		<-m.Changes()
	}
	before := p.count("1.1.1.1")
	time.Sleep(40 * time.Millisecond)
	if after := p.count("1.1.1.1"); after != before {
		t.Errorf("probed after going idle: %d -> %d", before, after)
	}
	if s := m.Status(); s.State != core.BaselineIdle || s.NetworkKey != "" {
		t.Errorf("status = %+v", s)
	}
}

// seed writes n rounds of history for key into the store, at rtt.
func seed(t *testing.T, st core.Store, key string, n int, rtt float64) {
	t.Helper()
	ctx := context.Background()
	start := time.Now().Add(-time.Duration(n) * 30 * time.Second)
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		for _, a := range []struct {
			anchor, addr string
			rtt          float64
		}{{GatewayAnchor, "192.168.1.1", 2}, {"1.1.1.1", "1.1.1.1", rtt}, {"8.8.8.8", "8.8.8.8", rtt + 1}} {
			s := core.Sample{Time: at, NetworkKey: key, Anchor: a.anchor, AnchorAddr: a.addr, RTTms: a.rtt, DNSms: -1, Method: MethodICMP}
			if err := st.AddSample(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestEngineTransitionSurfacesAsEvent(t *testing.T) {
	p := newScripted()
	p.set("192.168.1.1", 2)
	p.set("1.1.1.1", 60)
	p.set("8.8.8.8", 61)
	m, st := newTestMonitor(t, p, Config{Interval: 5 * time.Millisecond})
	seed(t, st, "wifi:Home", 40, 20) // warm history at 20 ms

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	m.SetNetwork("wifi:Home", "192.168.1.1")

	if s := m.Status(); s.State != core.BaselineOK || len(s.Anchors) != 3 || s.Anchors[1].SampleCount != 40 {
		t.Fatalf("status after warm-up = %+v, want ok with 40 samples", s)
	}

	var ev core.Event
	select {
	case ev = <-m.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}
	if ev.Type != core.EventDegraded || ev.NetworkKey != "wifi:Home" {
		t.Fatalf("event = %+v", ev)
	}
	want := "Latency to 1.1.1.1 is 60 ms, usually 20 ms on Home"
	if ev.Body != want {
		t.Errorf("body = %q, want %q", ev.Body, want)
	}
	if ev.Title != "Network degraded" || ev.Urgency != "normal" || ev.Data["anchor"] != "1.1.1.1" || ev.Data["reason"] != "rtt" {
		t.Errorf("event fields = %+v", ev)
	}
	if s := m.Status(); s.State != core.BaselineDegraded {
		t.Errorf("status state = %s, want degraded", s.State)
	}
	// The daemon persists what it reads from Events(); storing here as well
	// used to record every degraded/recovered event twice.
	stored, err := st.Events(ctx, 0)
	if err != nil || len(stored) != 0 {
		t.Errorf("monitor must not persist events itself: %+v err=%v", stored, err)
	}

	// Recovery: back to 20 ms, expect a recovered event.
	p.set("1.1.1.1", 20)
	p.set("8.8.8.8", 21)
	select {
	case ev = <-m.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("no recovery event within 5 s")
	}
	if ev.Type != core.EventRecovered || ev.Urgency != "low" {
		t.Fatalf("second event = %+v", ev)
	}
	if s := m.Status(); s.State != core.BaselineOK {
		t.Errorf("status state = %s, want ok", s.State)
	}
}

func TestResetBaseline(t *testing.T) {
	p := newScripted()
	m, st := newTestMonitor(t, p, Config{Interval: time.Hour})
	seed(t, st, "wifi:Home", 45, 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	m.SetNetwork("wifi:Home", "192.168.1.1")
	waitChange(t, m, "round", 2*time.Second)
	if s := m.Status(); s.State != core.BaselineOK {
		t.Fatalf("setup: state = %s", s.State)
	}
	if err := m.ResetBaseline(ctx, ""); err != nil {
		t.Fatal(err)
	}
	waitChange(t, m, "reset", time.Second)
	if s := m.Status(); s.State != core.BaselineIdle || len(s.Anchors) != 0 {
		t.Errorf("status after reset = %+v", s)
	}
	if bl, _ := st.Baselines(ctx, "wifi:Home"); len(bl) != 0 {
		t.Errorf("baselines survived reset: %+v", bl)
	}
	if n, _ := st.SampleCount(ctx); n == 0 {
		t.Error("reset must keep samples")
	}
}

func TestEventChannelDropsWhenFull(t *testing.T) {
	m, _ := newTestMonitor(t, newScripted(), Config{})
	for i := 0; i < eventBuffer+5; i++ {
		m.emitChange("x")
	}
	if len(m.Changes()) != eventBuffer {
		t.Errorf("buffered %d changes, want %d", len(m.Changes()), eventBuffer)
	}
}

func TestEventForBodies(t *testing.T) {
	b := core.Baseline{CurrentRTT: 64.4, BaselineRTT: 21.2, CurrentLoss: 0.15, BaselineLoss: 0.004}
	tests := []struct {
		name string
		tr   Transition
		typ  core.EventType
		body string
		ok   bool
	}{
		{"rtt", Transition{NetworkKey: "wifi:ALHN-F832-5", To: core.BaselineDegraded, From: core.BaselineOK, Anchor: "1.1.1.1", Reason: ReasonRTT, Baseline: b},
			core.EventDegraded, "Latency to 1.1.1.1 is 64 ms, usually 21 ms on ALHN-F832-5", true},
		{"loss", Transition{NetworkKey: "wired:u", To: core.BaselineDegraded, From: core.BaselineOK, Anchor: "8.8.8.8", Reason: ReasonLoss, Baseline: b},
			core.EventDegraded, "Packet loss to 8.8.8.8 is 15 %, usually 0 % on u", true},
		{"recovered", Transition{NetworkKey: "wifi:Home", To: core.BaselineOK, From: core.BaselineDegraded, Anchor: "1.1.1.1", Baseline: core.Baseline{CurrentRTT: 22}},
			core.EventRecovered, "Latency to 1.1.1.1 is back to 22 ms on Home", true},
		{"learning->ok is silent", Transition{NetworkKey: "wifi:Home", From: core.BaselineLearning, To: core.BaselineOK}, "", "", false},
		{"idle->learning is silent", Transition{NetworkKey: "wifi:Home", From: core.BaselineIdle, To: core.BaselineLearning}, "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := eventFor(tc.tr)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if ev.Type != tc.typ || ev.Body != tc.body {
				t.Errorf("event = %s %q, want %s %q", ev.Type, ev.Body, tc.typ, tc.body)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{"wifi:Home": "Home", "wired:abc": "abc", "plain": "plain", "x:": "x:"} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}
