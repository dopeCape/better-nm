package monitor

import (
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

const testKey = "wifi:Home"

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// series feeds samples for one anchor and returns the sample time cursor.
type feeder struct {
	e   *Engine
	key string
	n   int
	all []Transition
}

func newFeeder(e *Engine) *feeder { return &feeder{e: e, key: testKey} }

func (f *feeder) at() time.Time { return t0.Add(time.Duration(f.n) * 30 * time.Second) }

// add feeds one sample per anchor in rtts (anchor -> rtt), one "round".
func (f *feeder) round(rtts map[string]float64, losses map[string]float64) []Transition {
	var out []Transition
	at := f.at()
	f.n++
	// deterministic order: gateway first, then sorted anchors
	names := make([]string, 0, len(rtts))
	if _, ok := rtts[GatewayAnchor]; ok {
		names = append(names, GatewayAnchor)
	}
	for _, a := range []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"} {
		if _, ok := rtts[a]; ok {
			names = append(names, a)
		}
	}
	for _, a := range names {
		s := core.Sample{Time: at, NetworkKey: f.key, Anchor: a, RTTms: rtts[a], DNSms: -1, Method: "icmp"}
		if losses != nil {
			s.Loss = losses[a]
		}
		if s.RTTms < 0 {
			s.Loss = 1
		}
		out = append(out, f.e.Add(s)...)
	}
	f.all = append(f.all, out...)
	return out
}

// rounds feeds n identical rounds, returning all transitions.
func (f *feeder) rounds(n int, rtts map[string]float64, losses map[string]float64) []Transition {
	var out []Transition
	for i := 0; i < n; i++ {
		out = append(out, f.round(rtts, losses)...)
	}
	return out
}

func stable(rtt float64) map[string]float64 {
	return map[string]float64{GatewayAnchor: 2, "1.1.1.1": rtt, "8.8.8.8": rtt + 1}
}

func TestDefaultConfigIsTheTicket(t *testing.T) {
	c := NewEngine(EngineConfig{}).Config()
	want := EngineConfig{200, 40, 10, 2, 20, 0.10, 0.02, 3, 1.3, 0.03, 10}
	if c != want {
		t.Fatalf("defaults = %+v, want %+v", c, want)
	}
}

func TestNewKeyLearnsUntil40(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	if st, _ := e.State(testKey); st != core.BaselineIdle {
		t.Fatalf("unknown key state = %s, want idle", st)
	}
	tr := f.round(stable(20), nil)
	if len(tr) != 1 || tr[0].From != core.BaselineIdle || tr[0].To != core.BaselineLearning {
		t.Fatalf("first sample transitions = %+v, want idle->learning", tr)
	}
	tr = f.rounds(38, stable(20), nil) // 39 samples per anchor
	if len(tr) != 0 {
		t.Fatalf("unexpected transitions while learning: %+v", tr)
	}
	if st, _ := e.State(testKey); st != core.BaselineLearning {
		t.Fatalf("state after 39 = %s, want learning", st)
	}
	for _, b := range e.Baselines(testKey) {
		if b.State != core.BaselineLearning || b.SampleCount != 39 {
			t.Errorf("anchor %s = %s/%d, want learning/39", b.Anchor, b.State, b.SampleCount)
		}
	}
	tr = f.round(stable(20), nil) // 40th
	if len(tr) != 1 || tr[0].From != core.BaselineLearning || tr[0].To != core.BaselineOK {
		t.Fatalf("40th sample transitions = %+v, want learning->ok", tr)
	}
	if !tr[0].At.Equal(t0.Add(39 * 30 * time.Second)) {
		t.Errorf("At = %v", tr[0].At)
	}
	st, since := e.State(testKey)
	if st != core.BaselineOK || !since.Equal(tr[0].At) {
		t.Errorf("state = %s since %v", st, since)
	}
	b := e.Baselines(testKey)
	if len(b) != 3 || b[0].Anchor != GatewayAnchor || b[1].Anchor != "1.1.1.1" || b[2].Anchor != "8.8.8.8" {
		t.Fatalf("baselines order: %+v", b)
	}
	if b[1].BaselineRTT != 20 || b[1].CurrentRTT != 20 || b[1].State != core.BaselineOK || b[1].BaselineLoss != 0 {
		t.Errorf("1.1.1.1 baseline = %+v", b[1])
	}
}

func TestStableNetworkStaysOK(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)
	// Jitter around the baseline, well inside the rule.
	var tr []Transition
	for i := 0; i < 400; i++ {
		r := stable(20 + float64(i%7) - 3)                                                // 17..23
		tr = append(tr, f.round(r, map[string]float64{"1.1.1.1": float64(i%20) / 60})...) // occasional 1/3 loss
	}
	if len(tr) != 0 {
		t.Fatalf("stable network produced transitions: %+v", tr)
	}
	if st, _ := e.State(testKey); st != core.BaselineOK {
		t.Fatalf("state = %s, want ok", st)
	}
	b := e.Baselines(testKey)[1]
	if b.SampleCount != 200 {
		t.Errorf("window not capped: %d", b.SampleCount)
	}
}

// stepTo returns the number of rounds at the new RTT until a transition.
func stepTo(t *testing.T, f *feeder, rtts map[string]float64, max int) (int, Transition) {
	t.Helper()
	for i := 1; i <= max; i++ {
		if tr := f.round(rtts, nil); len(tr) > 0 {
			return i, tr[0]
		}
	}
	return 0, Transition{}
}

func TestStepDegradesAfterExactlyThreeEvaluations(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)

	// current = median of the last 10; with 6 of 10 at 60 ms it crosses
	// 2×20 and 20+20. Evaluation 1 is at the 6th sample, so the third
	// consecutive bad evaluation is the 8th sample at 60 ms.
	n, tr := stepTo(t, f, stable(60), 20)
	if n != 8 {
		t.Fatalf("degraded after %d samples at 60 ms, want 8 (6 to cross + 3 evaluations)", n)
	}
	if tr.From != core.BaselineOK || tr.To != core.BaselineDegraded {
		t.Fatalf("transition = %+v", tr)
	}
	if tr.Anchor != "1.1.1.1" || tr.Reason != ReasonRTT {
		t.Errorf("anchor/reason = %s/%s, want 1.1.1.1/rtt", tr.Anchor, tr.Reason)
	}
	if tr.Baseline.CurrentRTT != 60 || tr.Baseline.BaselineRTT != 20 {
		t.Errorf("baseline snapshot = %+v", tr.Baseline)
	}
	st, since := e.State(testKey)
	if st != core.BaselineDegraded || !since.Equal(tr.At) {
		t.Errorf("state = %s since %v, want degraded since %v", st, since, tr.At)
	}
}

func TestExactlyThreeConsecutiveEvaluations(t *testing.T) {
	// Use a tiny current window so a single sample flips the evaluation:
	// two bad, one good, two bad must not degrade; three bad must.
	e := NewEngine(EngineConfig{CurrentWindow: 1})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)
	only := func(rtt float64) map[string]float64 { return map[string]float64{"1.1.1.1": rtt} }
	for _, rtt := range []float64{60, 60, 20, 60, 60} {
		if tr := f.round(only(rtt), nil); len(tr) != 0 {
			t.Fatalf("premature transition at %v: %+v", rtt, tr)
		}
	}
	tr := f.round(only(60), nil)
	if len(tr) != 1 || tr[0].To != core.BaselineDegraded {
		t.Fatalf("third consecutive bad evaluation should degrade, got %+v", tr)
	}
}

func TestSingleSpikeDoesNotDegrade(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(60, stable(20), nil)
	spike := stable(20)
	spike["1.1.1.1"] = 500
	spike["8.8.8.8"] = 500
	spike[GatewayAnchor] = 500
	tr := f.round(spike, nil)
	tr = append(tr, f.rounds(30, stable(20), nil)...)
	if len(tr) != 0 {
		t.Fatalf("spike produced transitions: %+v", tr)
	}
	if st, _ := e.State(testKey); st != core.BaselineOK {
		t.Fatalf("state = %s, want ok", st)
	}
}

func TestFifteenPercentLossDegrades(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(200, stable(20), nil) // a full, clean window
	loss := map[string]float64{"1.1.1.1": 0.15}
	var got *Transition
	for i := 1; i <= 20 && got == nil; i++ {
		if tr := f.round(stable(20), loss); len(tr) > 0 {
			got = &tr[0]
		}
	}
	if got == nil {
		t.Fatal("15 % loss never degraded")
	}
	if got.To != core.BaselineDegraded || got.Reason != ReasonLoss || got.Anchor != "1.1.1.1" {
		t.Fatalf("transition = %+v", *got)
	}
	if got.Baseline.CurrentLoss <= 0.10 || got.Baseline.BaselineLoss >= 0.02 {
		t.Errorf("snapshot loss = cur %v base %v", got.Baseline.CurrentLoss, got.Baseline.BaselineLoss)
	}
}

func TestSmallLossDoesNotDegrade(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(200, stable(20), nil)
	// 1 of 3 echoes lost every round = 33 % per sample... but the rule is on
	// the current mean, so 5 % per sample stays under 10 %.
	if tr := f.rounds(50, stable(20), map[string]float64{"1.1.1.1": 0.05}); len(tr) != 0 {
		t.Fatalf("5 %% loss produced transitions: %+v", tr)
	}
}

func TestRecoveryNeedsTenGoodSamples(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)
	if n, _ := stepTo(t, f, stable(60), 20); n == 0 {
		t.Fatal("did not degrade")
	}
	// Back to 20 ms: the current median falls under 1.3×20 once 6 of the
	// last 10 are 20 ms again (median of {20×5,60×5} = 40, not good), so
	// good evaluations start at the 6th sample; 10 consecutive → 15th.
	var recoveredAt int
	var tr Transition
	for i := 1; i <= 40; i++ {
		if out := f.round(stable(20), nil); len(out) > 0 {
			recoveredAt, tr = i, out[0]
			break
		}
	}
	if recoveredAt != 15 {
		t.Fatalf("recovered after %d good samples, want 15 (5 to fall back under 1.3× + 10 good)", recoveredAt)
	}
	// Both public anchors degraded; the key recovers when the last one does
	// (8.8.8.8 is fed after 1.1.1.1 in a round).
	if tr.From != core.BaselineDegraded || tr.To != core.BaselineOK || tr.Anchor != "8.8.8.8" {
		t.Fatalf("transition = %+v", tr)
	}
	if st, _ := e.State(testKey); st != core.BaselineOK {
		t.Fatalf("state = %s, want ok", st)
	}
	for _, b := range e.Baselines(testKey) {
		if b.State != core.BaselineOK {
			t.Errorf("anchor %s still %s", b.Anchor, b.State)
		}
	}
}

func TestRecoveryStreakResetsOnBadSample(t *testing.T) {
	e := NewEngine(EngineConfig{CurrentWindow: 1})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)
	only := func(rtt float64) map[string]float64 { return map[string]float64{"1.1.1.1": rtt} }
	f.rounds(3, only(60), nil)
	if st, _ := e.State(testKey); st != core.BaselineDegraded {
		t.Fatal("setup: not degraded")
	}
	f.rounds(9, only(20), nil) // 9 good
	f.round(only(60), nil)     // streak broken
	if tr := f.rounds(9, only(20), nil); len(tr) != 0 {
		t.Fatalf("recovered too early: %+v", tr)
	}
	if tr := f.round(only(20), nil); len(tr) != 1 || tr[0].To != core.BaselineOK {
		t.Fatalf("10th consecutive good sample should recover, got %+v", tr)
	}
}

func TestGatewayOnlyDegradationDoesNotTrigger(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)
	gwBad := stable(20)
	gwBad[GatewayAnchor] = 80
	if tr := f.rounds(30, gwBad, nil); len(tr) != 0 {
		t.Fatalf("gateway-only degradation transitioned: %+v", tr)
	}
	if st, _ := e.State(testKey); st != core.BaselineOK {
		t.Fatalf("state = %s, want ok", st)
	}
	// The gateway's own baseline still shows the trouble.
	var gw core.Baseline
	for _, b := range e.Baselines(testKey) {
		if b.Anchor == GatewayAnchor {
			gw = b
		}
	}
	if gw.State != core.BaselineDegraded {
		t.Errorf("gateway anchor state = %s, want degraded", gw.State)
	}
	// Once a public anchor agrees, the key degrades.
	both := gwBad
	both["1.1.1.1"] = 60
	n, tr := stepTo(t, f, both, 20)
	if n == 0 || tr.To != core.BaselineDegraded || tr.Anchor != "1.1.1.1" {
		t.Fatalf("public anchor agreeing should degrade: n=%d tr=%+v", n, tr)
	}
}

func TestPublicAnchorAloneTriggers(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)
	upstream := stable(20)
	upstream["8.8.8.8"] = 90
	// 21 -> 90: the median of 10 crosses at the 5th sample (midpoint 55.5 >
	// 2×21), so the third bad evaluation is the 7th sample.
	n, tr := stepTo(t, f, upstream, 20)
	if n != 7 || tr.To != core.BaselineDegraded || tr.Anchor != "8.8.8.8" {
		t.Fatalf("n=%d tr=%+v", n, tr)
	}
}

func TestAllLostSamplesDoNotPoisonMedian(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(40, stable(20), nil)
	lost := stable(20)
	lost["1.1.1.1"] = -1
	f.round(lost, nil)
	b := e.Baselines(testKey)[1]
	if b.BaselineRTT != 20 || b.CurrentRTT != 20 {
		t.Errorf("lost sample changed the medians: %+v", b)
	}
	if b.CurrentLoss != 0.1 {
		t.Errorf("current loss = %v, want 0.1", b.CurrentLoss)
	}
}

func TestResetForgetsKey(t *testing.T) {
	e := NewEngine(EngineConfig{})
	f := newFeeder(e)
	f.rounds(50, stable(20), nil)
	e.Reset(testKey)
	if st, _ := e.State(testKey); st != core.BaselineIdle {
		t.Fatalf("state after reset = %s", st)
	}
	if b := e.Baselines(testKey); b != nil {
		t.Fatalf("baselines after reset = %+v", b)
	}
	if tr := f.round(stable(20), nil); len(tr) != 1 || tr[0].To != core.BaselineLearning {
		t.Fatalf("after reset the key should be learning again: %+v", tr)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	e := NewEngine(EngineConfig{})
	home := newFeeder(e)
	work := &feeder{e: e, key: "wired:abc"}
	home.rounds(40, stable(20), nil)
	work.rounds(10, stable(5), nil)
	if st, _ := e.State(testKey); st != core.BaselineOK {
		t.Errorf("home = %s", st)
	}
	if st, _ := e.State("wired:abc"); st != core.BaselineLearning {
		t.Errorf("work = %s", st)
	}
	if n, _ := stepTo(t, home, stable(60), 20); n != 8 {
		t.Errorf("home degrade n = %d", n)
	}
	if st, _ := e.State("wired:abc"); st != core.BaselineLearning {
		t.Errorf("work affected by home: %s", st)
	}
}

func TestLoadWarmsFromStore(t *testing.T) {
	// Build history with one engine, then Load it into a fresh one.
	src := NewEngine(EngineConfig{})
	f := newFeeder(src)
	f.rounds(50, stable(20), nil)
	var samples []core.Sample
	for i := 0; i < 50; i++ {
		at := t0.Add(time.Duration(i) * 30 * time.Second)
		for a, rtt := range stable(20) {
			samples = append(samples, core.Sample{Time: at, NetworkKey: testKey, Anchor: a, RTTms: rtt, DNSms: -1})
		}
	}
	// shuffle-ish: reverse to prove Load sorts by time
	for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
		samples[i], samples[j] = samples[j], samples[i]
	}
	e := NewEngine(EngineConfig{})
	e.Load(src.Baselines(testKey), samples)

	st, since := e.State(testKey)
	if st != core.BaselineOK {
		t.Fatalf("state after load = %s, want ok", st)
	}
	if since.IsZero() {
		t.Error("since is zero after load")
	}
	got := e.Baselines(testKey)
	want := src.Baselines(testKey)
	if len(got) != len(want) {
		t.Fatalf("got %d baselines, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Anchor != want[i].Anchor || got[i].State != want[i].State ||
			got[i].SampleCount != want[i].SampleCount || got[i].BaselineRTT != want[i].BaselineRTT ||
			!got[i].Since.Equal(want[i].Since) {
			t.Errorf("anchor %s: got %+v, want %+v", got[i].Anchor, got[i], want[i])
		}
	}
	// And it keeps working: a step degrades exactly as on the source engine.
	f2 := &feeder{e: e, key: testKey, n: 50}
	if n, tr := stepTo(t, f2, stable(60), 20); n != 8 || tr.To != core.BaselineDegraded {
		t.Errorf("after load: n=%d tr=%+v", n, tr)
	}
}

func TestLoadDegradedStateSurvivesRestart(t *testing.T) {
	src := NewEngine(EngineConfig{})
	f := newFeeder(src)
	f.rounds(40, stable(20), nil)
	stepTo(t, f, stable(60), 20)
	e := NewEngine(EngineConfig{})
	var samples []core.Sample
	for i := 0; i < 48; i++ {
		rtt := 20.0
		if i >= 40 {
			rtt = 60
		}
		at := t0.Add(time.Duration(i) * 30 * time.Second)
		samples = append(samples, core.Sample{Time: at, NetworkKey: testKey, Anchor: "1.1.1.1", RTTms: rtt, DNSms: -1})
		samples = append(samples, core.Sample{Time: at, NetworkKey: testKey, Anchor: GatewayAnchor, RTTms: 2, DNSms: -1})
	}
	e.Load(src.Baselines(testKey), samples)
	if st, _ := e.State(testKey); st != core.BaselineDegraded {
		t.Fatalf("state after load = %s, want degraded", st)
	}
	// Baselines with too few samples fall back to learning.
	e2 := NewEngine(EngineConfig{})
	e2.Load([]core.Baseline{{NetworkKey: testKey, Anchor: "1.1.1.1", State: core.BaselineOK}}, samples[:10])
	if b := e2.Baselines(testKey); len(b) != 2 || b[1].State != core.BaselineLearning {
		t.Errorf("few samples should be learning: %+v", b)
	}
}

func TestLoadIgnoresGarbage(t *testing.T) {
	e := NewEngine(EngineConfig{})
	e.Load([]core.Baseline{{Anchor: "x"}}, []core.Sample{{NetworkKey: "k"}, {Anchor: "a"}})
	if len(e.keys) != 0 {
		t.Fatalf("garbage created keys: %v", e.keys)
	}
	if tr := e.Add(core.Sample{}); tr != nil {
		t.Fatalf("empty sample produced transitions: %+v", tr)
	}
}

func TestMedian(t *testing.T) {
	tests := []struct {
		in   []float64
		want float64
	}{
		{nil, -1},
		{[]float64{5}, 5},
		{[]float64{3, 1}, 2},
		{[]float64{9, 1, 5}, 5},
		{[]float64{4, 1, 3, 2}, 2.5},
	}
	for _, tc := range tests {
		if got := median(tc.in); got != tc.want {
			t.Errorf("median(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
