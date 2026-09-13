package monitor

import (
	"sort"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// GatewayAnchor is the anchor name of the default gateway.
const GatewayAnchor = "gateway"

// EngineConfig holds the baseline algorithm's knobs. The defaults are the
// numbers fixed by dopeCape/better-nm#14; tests may shrink them.
type EngineConfig struct {
	Window        int // rolling window per (key, anchor); baseline = its median RTT / mean loss
	MinSamples    int // "learning" until this many samples exist
	CurrentWindow int // "current" = median RTT / mean loss of the last N samples

	DegradeRTTFactor    float64 // current > factor × baseline ...
	DegradeRTTDeltaMs   float64 // ... and current > baseline + delta
	DegradeLoss         float64 // or current loss > this ...
	DegradeBaselineLoss float64 // ... while baseline loss < this
	DegradeStreak       int     // consecutive bad evaluations before an anchor is degraded

	RecoverRTTFactor float64 // current < factor × baseline ...
	RecoverLoss      float64 // ... and current loss < this
	RecoverStreak    int     // consecutive good samples before an anchor recovers
}

// DefaultEngineConfig returns the ticket's numbers.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		Window:              200,
		MinSamples:          40,
		CurrentWindow:       10,
		DegradeRTTFactor:    2,
		DegradeRTTDeltaMs:   20,
		DegradeLoss:         0.10,
		DegradeBaselineLoss: 0.02,
		DegradeStreak:       3,
		RecoverRTTFactor:    1.3,
		RecoverLoss:         0.03,
		RecoverStreak:       10,
	}
}

func (c EngineConfig) withDefaults() EngineConfig {
	d := DefaultEngineConfig()
	if c.Window <= 0 {
		c.Window = d.Window
	}
	if c.MinSamples <= 0 {
		c.MinSamples = d.MinSamples
	}
	if c.CurrentWindow <= 0 {
		c.CurrentWindow = d.CurrentWindow
	}
	if c.DegradeRTTFactor <= 0 {
		c.DegradeRTTFactor = d.DegradeRTTFactor
	}
	if c.DegradeRTTDeltaMs <= 0 {
		c.DegradeRTTDeltaMs = d.DegradeRTTDeltaMs
	}
	if c.DegradeLoss <= 0 {
		c.DegradeLoss = d.DegradeLoss
	}
	if c.DegradeBaselineLoss <= 0 {
		c.DegradeBaselineLoss = d.DegradeBaselineLoss
	}
	if c.DegradeStreak <= 0 {
		c.DegradeStreak = d.DegradeStreak
	}
	if c.RecoverRTTFactor <= 0 {
		c.RecoverRTTFactor = d.RecoverRTTFactor
	}
	if c.RecoverLoss <= 0 {
		c.RecoverLoss = d.RecoverLoss
	}
	if c.RecoverStreak <= 0 {
		c.RecoverStreak = d.RecoverStreak
	}
	if c.MinSamples > c.Window {
		c.MinSamples = c.Window
	}
	return c
}

// Reason says which rule fired for a transition.
type Reason string

const (
	ReasonRTT  Reason = "rtt"
	ReasonLoss Reason = "loss"
	ReasonNone Reason = ""
)

// Transition is a change of a Network Key's overall state.
type Transition struct {
	NetworkKey string
	From, To   core.BaselineState
	At         time.Time
	// Anchor is the anchor that decided the transition (the public anchor
	// whose streak completed, or the last one to recover). Empty for
	// learning→ok.
	Anchor string
	Reason Reason
	// Baseline is a snapshot of that anchor after the sample was applied.
	Baseline core.Baseline
}

// Engine is the pure baseline engine: feed it samples, get transitions back.
// It performs no I/O and never reads the clock; all timestamps come from the
// samples. It is not safe for concurrent use; Monitor serialises calls.
type Engine struct {
	cfg  EngineConfig
	keys map[string]*keyState
}

type keyState struct {
	state   core.BaselineState
	since   time.Time
	anchors map[string]*anchorState
}

type anchorState struct {
	samples    []core.Sample // oldest first, at most cfg.Window
	state      core.BaselineState
	since      time.Time
	badStreak  int
	goodStreak int
	lastReason Reason
	baseline   core.Baseline // recomputed after every Add
}

// NewEngine creates an engine; zero fields in cfg take the ticket's defaults.
func NewEngine(cfg EngineConfig) *Engine {
	return &Engine{cfg: cfg.withDefaults(), keys: map[string]*keyState{}}
}

// Config returns the effective configuration.
func (e *Engine) Config() EngineConfig { return e.cfg }

// Add applies one sample and returns any key-level transitions it caused.
func (e *Engine) Add(s core.Sample) []Transition {
	if s.NetworkKey == "" || s.Anchor == "" {
		return nil
	}
	ks := e.key(s.NetworkKey)
	as, ok := ks.anchors[s.Anchor]
	if !ok {
		as = &anchorState{state: core.BaselineLearning, since: s.Time}
		ks.anchors[s.Anchor] = as
	}
	as.samples = append(as.samples, s)
	if n := len(as.samples) - e.cfg.Window; n > 0 {
		as.samples = append([]core.Sample(nil), as.samples[n:]...)
	}
	e.evaluate(as, s.Time)
	as.baseline = e.snapshot(s.NetworkKey, s.Anchor, as, s.Time)
	return e.settle(s.NetworkKey, ks, s.Anchor, as, s.Time)
}

// evaluate updates the anchor's own state machine after a new sample.
func (e *Engine) evaluate(as *anchorState, at time.Time) {
	if len(as.samples) < e.cfg.MinSamples {
		if as.state != core.BaselineLearning {
			as.state = core.BaselineLearning
			as.since = at
		}
		as.badStreak, as.goodStreak = 0, 0
		return
	}
	baseRTT, baseLoss := e.baselineStats(as.samples)
	curRTT, curLoss, _ := e.currentStats(as.samples)

	rttBad := curRTT >= 0 && baseRTT >= 0 &&
		curRTT > e.cfg.DegradeRTTFactor*baseRTT &&
		curRTT > baseRTT+e.cfg.DegradeRTTDeltaMs
	lossBad := curLoss > e.cfg.DegradeLoss && baseLoss < e.cfg.DegradeBaselineLoss
	good := curRTT >= 0 && baseRTT >= 0 &&
		curRTT < e.cfg.RecoverRTTFactor*baseRTT &&
		curLoss < e.cfg.RecoverLoss

	switch as.state {
	case core.BaselineLearning, core.BaselineIdle:
		as.state = core.BaselineOK
		as.since = at
		as.badStreak, as.goodStreak = 0, 0
		fallthrough
	case core.BaselineOK:
		if rttBad || lossBad {
			as.badStreak++
			if lossBad && !rttBad {
				as.lastReason = ReasonLoss
			} else {
				as.lastReason = ReasonRTT
			}
		} else {
			as.badStreak = 0
		}
		if as.badStreak >= e.cfg.DegradeStreak {
			as.state = core.BaselineDegraded
			as.since = at
			as.goodStreak = 0
		}
	case core.BaselineDegraded:
		if good {
			as.goodStreak++
		} else {
			as.goodStreak = 0
		}
		if as.goodStreak >= e.cfg.RecoverStreak {
			as.state = core.BaselineOK
			as.since = at
			as.badStreak = 0
		}
	}
}

// settle derives the key-level state from its anchors and reports a change.
func (e *Engine) settle(key string, ks *keyState, anchor string, as *anchorState, at time.Time) []Transition {
	next := e.deriveKeyState(ks)
	if next == ks.state {
		return nil
	}
	prev := ks.state
	ks.state = next
	ks.since = at
	tr := Transition{NetworkKey: key, From: prev, To: next, At: at}
	if next == core.BaselineDegraded || prev == core.BaselineDegraded {
		tr.Anchor = anchor
		tr.Baseline = as.baseline
	}
	if next == core.BaselineDegraded {
		tr.Reason = as.lastReason
	}
	return []Transition{tr}
}

// deriveKeyState: degraded when any public anchor is degraded (the gateway
// alone never decides); learning while every anchor is still learning;
// ok otherwise.
func (e *Engine) deriveKeyState(ks *keyState) core.BaselineState {
	if len(ks.anchors) == 0 {
		return core.BaselineIdle
	}
	allLearning := true
	for name, as := range ks.anchors {
		if as.state != core.BaselineLearning {
			allLearning = false
		}
		if name != GatewayAnchor && as.state == core.BaselineDegraded {
			return core.BaselineDegraded
		}
	}
	if allLearning {
		return core.BaselineLearning
	}
	return core.BaselineOK
}

func (e *Engine) key(k string) *keyState {
	ks, ok := e.keys[k]
	if !ok {
		ks = &keyState{state: core.BaselineIdle, anchors: map[string]*anchorState{}}
		e.keys[k] = ks
	}
	return ks
}

// State returns the key's overall state and when it began.
func (e *Engine) State(networkKey string) (core.BaselineState, time.Time) {
	ks, ok := e.keys[networkKey]
	if !ok {
		return core.BaselineIdle, time.Time{}
	}
	return ks.state, ks.since
}

// Baselines returns the per-anchor baselines for a key, gateway first then by name.
func (e *Engine) Baselines(networkKey string) []core.Baseline {
	ks, ok := e.keys[networkKey]
	if !ok {
		return nil
	}
	out := make([]core.Baseline, 0, len(ks.anchors))
	for _, as := range ks.anchors {
		out = append(out, as.baseline)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Anchor == GatewayAnchor) != (out[j].Anchor == GatewayAnchor) {
			return out[i].Anchor == GatewayAnchor
		}
		return out[i].Anchor < out[j].Anchor
	})
	return out
}

// Reset forgets everything about a key.
func (e *Engine) Reset(networkKey string) {
	delete(e.keys, networkKey)
}

// Load warms the engine from stored baselines and samples (any order, any
// keys). Stored states and Since stamps are kept; streak counters restart
// at zero, so a degraded anchor needs a full recovery streak after restart.
func (e *Engine) Load(baselines []core.Baseline, samples []core.Sample) {
	sorted := append([]core.Sample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })
	touched := map[string]bool{}
	for _, s := range sorted {
		if s.NetworkKey == "" || s.Anchor == "" {
			continue
		}
		ks := e.key(s.NetworkKey)
		as, ok := ks.anchors[s.Anchor]
		if !ok {
			as = &anchorState{state: core.BaselineLearning, since: s.Time}
			ks.anchors[s.Anchor] = as
		}
		as.samples = append(as.samples, s)
		touched[s.NetworkKey] = true
	}
	for _, b := range baselines {
		if b.NetworkKey == "" || b.Anchor == "" {
			continue
		}
		ks := e.key(b.NetworkKey)
		as, ok := ks.anchors[b.Anchor]
		if !ok {
			as = &anchorState{}
			ks.anchors[b.Anchor] = as
		}
		if b.State != "" {
			as.state = b.State
		}
		as.since = b.Since
		touched[b.NetworkKey] = true
	}
	for key := range touched {
		ks := e.keys[key]
		var latest time.Time
		for name, as := range ks.anchors {
			if n := len(as.samples) - e.cfg.Window; n > 0 {
				as.samples = as.samples[n:]
			}
			if as.state == "" || as.state == core.BaselineIdle {
				as.state = core.BaselineLearning
			}
			// A stored "ok"/"degraded" with too few samples is still learning.
			if len(as.samples) < e.cfg.MinSamples && as.state != core.BaselineLearning {
				as.state = core.BaselineLearning
			}
			if len(as.samples) >= e.cfg.MinSamples && as.state == core.BaselineLearning {
				as.state = core.BaselineOK
			}
			at := as.since
			if len(as.samples) > 0 {
				at = as.samples[len(as.samples)-1].Time
			}
			if as.since.IsZero() {
				as.since = at
			}
			as.baseline = e.snapshot(key, name, as, at)
			if at.After(latest) {
				latest = at
			}
		}
		ks.state = e.deriveKeyState(ks)
		if ks.since.IsZero() {
			ks.since = latest
		}
	}
}

// snapshot builds the public Baseline for an anchor.
func (e *Engine) snapshot(key, anchor string, as *anchorState, at time.Time) core.Baseline {
	b := core.Baseline{
		NetworkKey:  key,
		Anchor:      anchor,
		State:       as.state,
		SampleCount: len(as.samples),
		Since:       as.since,
		UpdatedAt:   at,
		BaselineRTT: -1,
		CurrentRTT:  -1,
		CurrentDNS:  -1,
	}
	if len(as.samples) == 0 {
		return b
	}
	b.BaselineRTT, b.BaselineLoss = e.baselineStats(as.samples)
	b.CurrentRTT, b.CurrentLoss, b.CurrentDNS = e.currentStats(as.samples)
	return b
}

// baselineStats: median RTT of the answered samples and mean loss over the window.
func (e *Engine) baselineStats(samples []core.Sample) (rtt, loss float64) {
	return medianRTT(samples), meanLoss(samples)
}

// currentStats: the same over the last CurrentWindow samples, plus the most
// recent DNS measurement in that window (-1 if none).
func (e *Engine) currentStats(samples []core.Sample) (rtt, loss, dns float64) {
	cur := samples
	if len(cur) > e.cfg.CurrentWindow {
		cur = cur[len(cur)-e.cfg.CurrentWindow:]
	}
	dns = -1
	for i := len(cur) - 1; i >= 0; i-- {
		if cur[i].DNSms >= 0 {
			dns = cur[i].DNSms
			break
		}
	}
	return medianRTT(cur), meanLoss(cur), dns
}

func medianRTT(samples []core.Sample) float64 {
	vals := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.RTTms >= 0 {
			vals = append(vals, s.RTTms)
		}
	}
	return median(vals)
}

func meanLoss(samples []core.Sample) float64 {
	if len(samples) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range samples {
		sum += s.Loss
	}
	return sum / float64(len(samples))
}

// median returns -1 for an empty slice.
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return -1
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
