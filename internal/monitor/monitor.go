// Package monitor owns bnm's passive network monitoring: it probes the
// default gateway and a few public anchors every Interval, stores the
// samples, keeps a per-network baseline and emits degraded/recovered events
// when the network is meaningfully slower than its own history.
//
// Three pieces: Prober (ICMP over unprivileged ping sockets, TCP connect as
// fallback, Auto picks), Engine (the pure baseline algorithm fixed by
// dopeCape/better-nm#14) and Monitor (the loop that wires probes, store,
// engine and event channels together).
//
// Tests: the engine is exercised on synthetic series; the Monitor on a fake
// Prober with a :memory: store; TCPProber against a loopback listener. Real
// sockets are only touched by tests behind the `live` build tag.
package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Defaults for Config.
const (
	DefaultInterval = 30 * time.Second
	DefaultEchoes   = 3
	// DefaultProbeTimeout bounds one whole round.
	DefaultProbeTimeout = 20 * time.Second
	pruneEvery          = time.Hour
	eventBuffer         = 32
)

// DefaultAnchors are the public hosts probed next to the gateway.
var DefaultAnchors = []string{"1.1.1.1", "8.8.8.8"}

// Config tunes a Monitor.
type Config struct {
	Interval time.Duration // probe period (default 30 s)
	Anchors  []string      // public anchors (default 1.1.1.1, 8.8.8.8)
	Resolver string        // DNS server for the DNS probe; "" = system
	Engine   EngineConfig  // zero = ticket defaults
	// RoundTimeout bounds one round of probes (default 20 s).
	RoundTimeout time.Duration
	// DNS overrides the DNS probe (tests). nil = DNSProbe.
	DNS func(ctx context.Context, resolver string) (float64, error)
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.Anchors == nil {
		c.Anchors = append([]string(nil), DefaultAnchors...)
	}
	if c.RoundTimeout <= 0 {
		c.RoundTimeout = DefaultProbeTimeout
	}
	if c.DNS == nil {
		c.DNS = DNSProbe
	}
	return c
}

// Monitor runs the probe loop for the currently connected network.
type Monitor struct {
	cfg    Config
	st     core.Store
	prober Prober
	log    *slog.Logger

	mu         sync.Mutex
	engine     *Engine
	networkKey string
	gatewayIP  string
	paused     bool
	lastSample time.Time
	lastPrune  time.Time

	events  chan core.Event
	changes chan core.Change
	wake    chan struct{}
}

// New builds a Monitor; nil prober means NewAutoProber, nil logger slog.Default().
func New(cfg Config, st core.Store, prober Prober, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	if prober == nil {
		prober = NewAutoProber(logger)
	}
	return &Monitor{
		cfg:     cfg.withDefaults(),
		st:      st,
		prober:  prober,
		log:     logger.With("component", "monitor"),
		engine:  NewEngine(cfg.Engine),
		events:  make(chan core.Event, eventBuffer),
		changes: make(chan core.Change, eventBuffer),
		wake:    make(chan struct{}, 1),
	}
}

// Events delivers degraded/recovered events. Buffered; drops when full.
func (m *Monitor) Events() <-chan core.Event { return m.events }

// Changes delivers a ChangeMonitor hint after every round and state change.
func (m *Monitor) Changes() <-chan core.Change { return m.changes }

// SetNetwork tells the monitor which network is primary ("" = none, go
// idle) and its gateway. A new key is warmed from the store.
func (m *Monitor) SetNetwork(key, gatewayIP string) {
	m.mu.Lock()
	changed := key != m.networkKey
	m.networkKey = key
	m.gatewayIP = gatewayIP
	if changed && key != "" {
		m.warmLocked(key)
	}
	m.mu.Unlock()
	if changed {
		m.emitChange("network")
		m.poke()
	}
}

// warmLocked loads a key's history from the store into the engine.
func (m *Monitor) warmLocked(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.engine.Reset(key)
	baselines, err := m.st.Baselines(ctx, key)
	if err != nil {
		m.log.Warn("load baselines", "key", key, "err", err)
	}
	anchors := append([]string{GatewayAnchor}, m.cfg.Anchors...)
	var samples []core.Sample
	for _, a := range anchors {
		ss, err := m.st.Samples(ctx, key, a, m.engine.Config().Window)
		if err != nil {
			m.log.Warn("load samples", "key", key, "anchor", a, "err", err)
			continue
		}
		samples = append(samples, ss...)
	}
	m.engine.Load(baselines, samples)
	if len(samples) > 0 {
		m.lastSample = samples[len(samples)-1].Time
	}
	st, _ := m.engine.State(key)
	m.log.Debug("warmed baseline", "key", key, "samples", len(samples), "state", st)
}

// Pause stops probing until Resume; the state is kept.
func (m *Monitor) Pause() {
	m.mu.Lock()
	m.paused = true
	m.mu.Unlock()
	m.emitChange("paused")
}

// Resume restarts probing after Pause and triggers a round now.
func (m *Monitor) Resume() {
	m.mu.Lock()
	m.paused = false
	m.mu.Unlock()
	m.emitChange("resumed")
	m.poke()
}

// ResetBaseline forgets the learned baseline for key (samples are kept);
// key "" means the current network.
func (m *Monitor) ResetBaseline(ctx context.Context, key string) error {
	m.mu.Lock()
	if key == "" {
		key = m.networkKey
	}
	m.engine.Reset(key)
	m.mu.Unlock()
	if key == "" {
		return nil
	}
	if err := m.st.DeleteBaselines(ctx, key); err != nil {
		return fmt.Errorf("monitor: reset baseline %s: %w", key, err)
	}
	m.emitChange("reset")
	return nil
}

// Samples returns stored samples for key/anchor, oldest first; key "" is the current network.
func (m *Monitor) Samples(ctx context.Context, key, anchor string, limit int) ([]core.Sample, error) {
	if key == "" {
		m.mu.Lock()
		key = m.networkKey
		m.mu.Unlock()
	}
	return m.st.Samples(ctx, key, anchor, limit)
}

// Status is the summary the surfaces show.
func (m *Monitor) Status() core.MonitorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := core.MonitorStatus{
		NetworkKey: m.networkKey,
		State:      core.BaselineIdle,
		LastSample: m.lastSample,
		Interval:   m.cfg.Interval,
		Paused:     m.paused,
	}
	if m.networkKey != "" {
		st.State, _ = m.engine.State(m.networkKey)
		if st.State == "" {
			st.State = core.BaselineIdle
		}
		st.Anchors = m.engine.Baselines(m.networkKey)
	}
	return st
}

// Run probes every Interval until ctx ends (a SetNetwork/Resume also
// triggers a round right away). It never returns early.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	m.maybePrune(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
		m.maybePrune(ctx)
		m.round(ctx)
	}
}

// RunOnce performs a single probe round (surfaces' "probe now").
func (m *Monitor) RunOnce(ctx context.Context) { m.round(ctx) }

func (m *Monitor) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Monitor) maybePrune(ctx context.Context) {
	m.mu.Lock()
	due := time.Since(m.lastPrune) >= pruneEvery
	if due {
		m.lastPrune = time.Now()
	}
	m.mu.Unlock()
	if !due {
		return
	}
	if err := m.st.Prune(ctx); err != nil && ctx.Err() == nil {
		m.log.Warn("prune", "err", err)
	}
}

type probeTarget struct {
	anchor string
	addr   string
}

// round probes every anchor once, stores the samples, feeds the engine.
func (m *Monitor) round(ctx context.Context) {
	m.mu.Lock()
	key, gw, paused := m.networkKey, m.gatewayIP, m.paused
	m.mu.Unlock()
	if key == "" || paused || ctx.Err() != nil {
		return
	}

	var targets []probeTarget
	if gw != "" {
		targets = append(targets, probeTarget{GatewayAnchor, gw})
	}
	for _, a := range m.cfg.Anchors {
		targets = append(targets, probeTarget{a, a})
	}
	if len(targets) == 0 {
		return
	}

	rctx, cancel := context.WithTimeout(ctx, m.cfg.RoundTimeout)
	defer cancel()

	now := time.Now()
	samples := make([]core.Sample, len(targets))
	var dnsMs float64 = -1
	var wg sync.WaitGroup
	wg.Add(len(targets) + 1)
	go func() {
		defer wg.Done()
		ms, err := m.cfg.DNS(rctx, m.cfg.Resolver)
		if err != nil {
			if rctx.Err() == nil {
				m.log.Debug("dns probe", "err", err)
			}
			return
		}
		dnsMs = ms
	}()
	for i, t := range targets {
		go func(i int, t probeTarget) {
			defer wg.Done()
			rtt, loss, method, err := m.prober.Probe(rctx, t.addr)
			if err != nil {
				if rctx.Err() == nil {
					m.log.Warn("probe", "anchor", t.anchor, "addr", t.addr, "err", err)
				}
				rtt, loss = -1, 1
			}
			samples[i] = core.Sample{
				Time: now, NetworkKey: key, Anchor: t.anchor, AnchorAddr: t.addr,
				RTTms: rtt, Loss: loss, DNSms: -1, Method: method,
			}
		}(i, t)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // shutting down: do not store a half round
	}
	samples[0].DNSms = dnsMs

	m.mu.Lock()
	if m.networkKey != key {
		m.mu.Unlock()
		return // network changed under us; drop the round
	}
	var transitions []Transition
	for _, s := range samples {
		if err := m.st.AddSample(ctx, s); err != nil {
			m.log.Warn("store sample", "anchor", s.Anchor, "err", err)
		}
		transitions = append(transitions, m.engine.Add(s)...)
	}
	m.lastSample = now
	for _, b := range m.engine.Baselines(key) {
		if err := m.st.PutBaseline(ctx, b); err != nil {
			m.log.Warn("store baseline", "anchor", b.Anchor, "err", err)
		}
	}
	state, _ := m.engine.State(key)
	m.mu.Unlock()

	for _, s := range samples {
		m.log.Debug("sample", "anchor", s.Anchor, "rtt_ms", round1(s.RTTms), "loss", s.Loss, "dns_ms", round1(s.DNSms), "method", s.Method)
	}
	m.log.Debug("round", "key", key, "state", state)

	// Events are emitted, not stored: the daemon owns history and persists
	// what it reads from Events() (storing here too wrote every event twice).
	for _, tr := range transitions {
		if ev, ok := eventFor(tr); ok {
			m.log.Info(ev.Title, "key", key, "body", ev.Body)
			select {
			case m.events <- ev:
			default:
				m.log.Warn("event dropped: consumer too slow", "type", ev.Type)
			}
		}
	}
	m.emitChange("round")
}

func (m *Monitor) emitChange(path string) {
	select {
	case m.changes <- core.Change{Kind: core.ChangeMonitor, Path: path}:
	default:
	}
}

// eventFor turns a key-level transition into a user-facing event.
func eventFor(tr Transition) (core.Event, bool) {
	b := tr.Baseline
	data := map[string]string{
		"anchor":          tr.Anchor,
		"from":            string(tr.From),
		"to":              string(tr.To),
		"reason":          string(tr.Reason),
		"current_rtt_ms":  fmtMs(b.CurrentRTT),
		"baseline_rtt_ms": fmtMs(b.BaselineRTT),
		"current_loss":    fmtPct(b.CurrentLoss),
		"baseline_loss":   fmtPct(b.BaselineLoss),
	}
	ev := core.Event{Time: tr.At, NetworkKey: tr.NetworkKey, Data: data}
	name := displayName(tr.NetworkKey)
	switch {
	case tr.To == core.BaselineDegraded:
		ev.Type = core.EventDegraded
		ev.Title = "Network degraded"
		ev.Urgency = "normal"
		if tr.Reason == ReasonLoss {
			ev.Body = fmt.Sprintf("Packet loss to %s is %s, usually %s on %s",
				tr.Anchor, fmtPct(b.CurrentLoss), fmtPct(b.BaselineLoss), name)
		} else {
			ev.Body = fmt.Sprintf("Latency to %s is %s ms, usually %s ms on %s",
				tr.Anchor, fmtMs(b.CurrentRTT), fmtMs(b.BaselineRTT), name)
		}
		return ev, true
	case tr.From == core.BaselineDegraded && tr.To == core.BaselineOK:
		ev.Type = core.EventRecovered
		ev.Title = "Network recovered"
		ev.Urgency = "low"
		ev.Body = fmt.Sprintf("Latency to %s is back to %s ms on %s", tr.Anchor, fmtMs(b.CurrentRTT), name)
		return ev, true
	}
	return core.Event{}, false
}

// displayName strips the key's type prefix: "wifi:Home" -> "Home".
func displayName(key string) string {
	if i := strings.IndexByte(key, ':'); i > 0 && i < len(key)-1 {
		return key[i+1:]
	}
	return key
}

func fmtMs(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f", v)
}

func fmtPct(v float64) string {
	return fmt.Sprintf("%.0f %%", v*100)
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
