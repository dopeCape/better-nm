package fake

import (
	"context"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Monitor stands in for internal/monitor: it records SetNetwork calls, serves
// a settable status and lets tests push degraded/recovered events.
type Monitor struct {
	mu       sync.Mutex
	status   core.MonitorStatus
	networks []NetworkCall
	samples  []core.Sample
	events   chan core.Event
	changes  chan core.Change
	resets   []string
	running  bool
}

// NetworkCall is one SetNetwork invocation.
type NetworkCall struct{ Key, Gateway string }

// NewMonitor returns an idle monitor with a 30 s interval.
func NewMonitor() *Monitor {
	return &Monitor{
		status:  core.MonitorStatus{State: core.BaselineIdle, Interval: 30 * time.Second},
		events:  make(chan core.Event, 64),
		changes: make(chan core.Change, 64),
	}
}

func (m *Monitor) Run(ctx context.Context) error {
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	<-ctx.Done()
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
	return nil
}

// Running reports whether Run is in progress.
func (m *Monitor) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *Monitor) SetNetwork(networkKey, gateway string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.networks = append(m.networks, NetworkCall{networkKey, gateway})
	if networkKey != "" && m.status.NetworkKey == networkKey && len(m.status.Anchors) > 0 {
		// Same network again: keep whatever state was seeded, like the real
		// monitor keeps its warm baselines. Avoids racing tests that SetStatus
		// before the daemon's startup SetNetwork lands.
		return
	}
	m.status.NetworkKey = networkKey
	if networkKey == "" {
		m.status.State = core.BaselineIdle
		m.status.Anchors = nil
	} else {
		m.status.State = core.BaselineLearning
		m.status.Anchors = []core.Baseline{
			{NetworkKey: networkKey, Anchor: "gateway", State: core.BaselineLearning, UpdatedAt: time.Now()},
			{NetworkKey: networkKey, Anchor: "1.1.1.1", State: core.BaselineLearning, UpdatedAt: time.Now()},
		}
	}
	select {
	case m.changes <- core.Change{Kind: core.ChangeMonitor, Path: networkKey}:
	default:
	}
}

// Networks returns the SetNetwork history.
func (m *Monitor) Networks() []NetworkCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]NetworkCall(nil), m.networks...)
}

func (m *Monitor) Status() core.MonitorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// SetStatus replaces the status and emits a monitor change.
func (m *Monitor) SetStatus(s core.MonitorStatus) {
	m.mu.Lock()
	m.status = s
	m.mu.Unlock()
	select {
	case m.changes <- core.Change{Kind: core.ChangeMonitor}:
	default:
	}
}

func (m *Monitor) Pause() {
	m.mu.Lock()
	m.status.Paused = true
	m.mu.Unlock()
	select {
	case m.changes <- core.Change{Kind: core.ChangeMonitor, Path: "paused"}:
	default:
	}
}

func (m *Monitor) Resume() {
	m.mu.Lock()
	m.status.Paused = false
	m.mu.Unlock()
	select {
	case m.changes <- core.Change{Kind: core.ChangeMonitor, Path: "resumed"}:
	default:
	}
}

func (m *Monitor) ResetBaseline(ctx context.Context, networkKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if networkKey == "" {
		return core.Errorf(core.KindInvalid, "", "monitor: network key is required")
	}
	m.resets = append(m.resets, networkKey)
	for i := range m.status.Anchors {
		if m.status.Anchors[i].NetworkKey == networkKey {
			m.status.Anchors[i].State = core.BaselineLearning
			m.status.Anchors[i].SampleCount = 0
		}
	}
	return nil
}

// Resets returns the keys ResetBaseline was called with.
func (m *Monitor) Resets() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.resets...)
}

func (m *Monitor) Samples(ctx context.Context, networkKey, anchor string, limit int) ([]core.Sample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.Sample
	for _, s := range m.samples {
		if (networkKey == "" || s.NetworkKey == networkKey) && (anchor == "" || s.Anchor == anchor) {
			out = append(out, s)
		}
	}
	return tail(out, limit), nil
}

// AddSample seeds a sample for Samples to return.
func (m *Monitor) AddSample(s core.Sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, s)
}

func (m *Monitor) Events() <-chan core.Event   { return m.events }
func (m *Monitor) Changes() <-chan core.Change { return m.changes }

// Emit pushes a monitor event (degraded/recovered) to the daemon.
func (m *Monitor) Emit(e core.Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	select {
	case m.events <- e:
	default:
	}
}

// SpeedTester returns a canned result after a few progress callbacks.
type SpeedTester struct {
	mu sync.Mutex
	// Delay between progress steps; keep tiny in tests.
	Delay time.Duration
	// Err, when set, is returned instead of a result.
	Err  error
	runs int
}

// NewSpeedTester returns a tester that finishes immediately.
func NewSpeedTester() *SpeedTester { return &SpeedTester{} }

func (s *SpeedTester) Run(ctx context.Context, opts core.SpeedOptions, progress func(core.SpeedProgress)) (core.SpeedResult, error) {
	s.mu.Lock()
	s.runs++
	delay, err := s.Delay, s.Err
	s.mu.Unlock()
	if err != nil {
		return core.SpeedResult{}, err
	}
	steps := []core.SpeedProgress{
		{Phase: "latency", Percent: 10},
		{Phase: "download", Mbps: 120.5, Percent: 40, Bytes: 12_000_000},
		{Phase: "download", Mbps: 240.1, Percent: 60, Bytes: 40_000_000},
		{Phase: "upload", Mbps: 18.3, Percent: 90, Bytes: 4_000_000},
		{Phase: "done", Percent: 100},
	}
	start := time.Now()
	for _, p := range steps {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return core.SpeedResult{}, ctx.Err()
			case <-time.After(delay):
			}
		} else if ctx.Err() != nil {
			return core.SpeedResult{}, ctx.Err()
		}
		if progress != nil {
			progress(p)
		}
	}
	provider := opts.Provider
	if provider == "" {
		provider = "cloudflare"
	}
	return core.SpeedResult{
		Time: start, NetworkKey: opts.NetworkKey, Provider: provider, Server: opts.Server,
		DownloadMbps: 240.1, UploadMbps: 18.3, LatencyMs: 12.4, JitterMs: 1.1, BytesMoved: 44_000_000,
		Duration: time.Since(start), Quick: opts.Quick,
	}, nil
}

// Runs counts completed or attempted runs.
func (s *SpeedTester) Runs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs
}
