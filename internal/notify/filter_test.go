package notify

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// fakeClock is a manual clock whose AfterFunc timers fire on Advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	when    time.Time
	f       func()
	stopped bool
	fired   bool
}

func (t *fakeTimer) Stop() bool {
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{when: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves time forward and fires due timers in order, outside the lock.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.when.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].when.Before(due[j].when) })
	for _, t := range due {
		t.f()
	}
}

type recorder struct {
	mu     sync.Mutex
	events []core.Event
}

func (r *recorder) add(e core.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) types() []core.EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]core.EventType, len(r.events))
	for i, e := range r.events {
		out[i] = e.Type
	}
	return out
}

func ev(t core.EventType, key string) core.Event {
	return core.Event{Type: t, NetworkKey: key, Title: string(t)}
}

func setup(t *testing.T, p Policy) (*Filter, *fakeClock, *recorder) {
	t.Helper()
	clock := newFakeClock()
	f := NewFilterWithClock(p, clock)
	rec := &recorder{}
	f.OnReady(rec.add)
	return f, clock, rec
}

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()
	on := []core.EventType{core.EventConnected, core.EventDisconnected, core.EventNoInternet, core.EventInternetRestored, core.EventVPNUp, core.EventVPNDown}
	off := []core.EventType{core.EventDegraded, core.EventRecovered, core.EventWifiScan, core.EventStateChanged}
	for _, ty := range on {
		if !p.IsEnabled(ty) {
			t.Errorf("%s should be on", ty)
		}
	}
	for _, ty := range off {
		if p.IsEnabled(ty) {
			t.Errorf("%s should be off", ty)
		}
	}
	if p.Debounce != 5*time.Second || p.RateLimit != 30*time.Second {
		t.Errorf("timings = %v / %v", p.Debounce, p.RateLimit)
	}
}

func TestFilterDisabledAndMuted(t *testing.T) {
	p := DefaultPolicy()
	p.MutedNetworks = []string{"wifi:Cafe"}
	f, _, _ := setup(t, p)

	tests := []struct {
		e    core.Event
		want bool
	}{
		{ev(core.EventDegraded, "wifi:Home"), false},
		{ev(core.EventRecovered, "wifi:Home"), false},
		{ev(core.EventWifiScan, "wifi:Home"), false},
		{ev(core.EventNoInternet, "wifi:Cafe"), false},
		{ev(core.EventDisconnected, "wifi:Cafe"), false},
		{ev(core.EventNoInternet, "wifi:Home"), true},
		{ev(core.EventVPNUp, "tailscale:x"), true},
		{ev(core.EventVPNUp, ""), true}, // empty key is never muted
	}
	for _, tc := range tests {
		if got := f.Allow(tc.e); got != tc.want {
			t.Errorf("Allow(%s on %q) = %v, want %v", tc.e.Type, tc.e.NetworkKey, got, tc.want)
		}
	}
}

func TestFilterDebounceCancelsFlap(t *testing.T) {
	f, clock, rec := setup(t, DefaultPolicy())

	if f.Allow(ev(core.EventConnected, "wifi:Home")) {
		t.Fatal("connected must be held, not allowed synchronously")
	}
	if f.Pending() != 1 {
		t.Fatalf("pending = %d", f.Pending())
	}
	clock.Advance(2 * time.Second)
	if f.Allow(ev(core.EventDisconnected, "wifi:Home")) {
		t.Fatal("disconnected within the window must be dropped")
	}
	if f.Pending() != 0 {
		t.Fatalf("pending after flap = %d", f.Pending())
	}
	clock.Advance(10 * time.Second)
	if got := rec.types(); len(got) != 0 {
		t.Fatalf("nothing should be delivered, got %v", got)
	}
	// After the flap a fresh connect is held and delivered normally.
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	clock.Advance(5 * time.Second)
	if got := rec.types(); len(got) != 1 || got[0] != core.EventConnected {
		t.Fatalf("got %v, want [connected]", got)
	}
}

func TestFilterDebounceReleases(t *testing.T) {
	f, clock, rec := setup(t, DefaultPolicy())
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	clock.Advance(4999 * time.Millisecond)
	if len(rec.types()) != 0 {
		t.Fatal("released too early")
	}
	clock.Advance(1 * time.Millisecond)
	if got := rec.types(); len(got) != 1 || got[0] != core.EventConnected {
		t.Fatalf("got %v", got)
	}
	// A disconnect after the window is a real one.
	clock.Advance(time.Second)
	if !f.Allow(ev(core.EventDisconnected, "wifi:Home")) {
		t.Fatal("disconnected after the window must pass")
	}
}

func TestFilterDebounceIsPerNetwork(t *testing.T) {
	f, clock, rec := setup(t, DefaultPolicy())
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	if !f.Allow(ev(core.EventDisconnected, "wifi:Office")) {
		t.Fatal("disconnect on another key must not be swallowed")
	}
	clock.Advance(5 * time.Second)
	if got := rec.types(); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestFilterCoalescesRepeatedConnects(t *testing.T) {
	f, clock, rec := setup(t, DefaultPolicy())
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	clock.Advance(time.Second)
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	clock.Advance(5 * time.Second)
	if got := rec.types(); len(got) != 1 {
		t.Fatalf("got %v, want one connected", got)
	}
}

func TestFilterRateLimit(t *testing.T) {
	f, clock, _ := setup(t, DefaultPolicy())
	if !f.Allow(ev(core.EventNoInternet, "wifi:Home")) {
		t.Fatal("first must pass")
	}
	clock.Advance(10 * time.Second)
	if f.Allow(ev(core.EventNoInternet, "wifi:Home")) {
		t.Fatal("second within 30 s must be dropped")
	}
	if !f.Allow(ev(core.EventInternetRestored, "wifi:Home")) {
		t.Fatal("different type is not limited")
	}
	if !f.Allow(ev(core.EventNoInternet, "wifi:Office")) {
		t.Fatal("different key is not limited")
	}
	clock.Advance(20 * time.Second)
	if !f.Allow(ev(core.EventNoInternet, "wifi:Home")) {
		t.Fatal("after 30 s it passes again")
	}
}

func TestFilterRateLimitAppliesToHeldConnects(t *testing.T) {
	f, clock, rec := setup(t, DefaultPolicy())
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	clock.Advance(5 * time.Second) // delivered at t=5
	clock.Advance(10 * time.Second)
	f.Allow(ev(core.EventConnected, "wifi:Home")) // t=15, 10 s after delivery -> limited
	clock.Advance(5 * time.Second)
	if got := rec.types(); len(got) != 1 {
		t.Fatalf("got %v, want one connected", got)
	}
	clock.Advance(20 * time.Second) // t=40
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	clock.Advance(5 * time.Second)
	if got := rec.types(); len(got) != 2 {
		t.Fatalf("got %v, want two connected", got)
	}
}

func TestFilterFlush(t *testing.T) {
	f, clock, rec := setup(t, DefaultPolicy())
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	f.Allow(ev(core.EventConnected, "wifi:Office"))
	f.Flush()
	got := rec.types()
	if len(got) != 2 {
		t.Fatalf("flush delivered %v", got)
	}
	if f.Pending() != 0 {
		t.Fatal("still pending after flush")
	}
	clock.Advance(10 * time.Second)
	if len(rec.types()) != 2 {
		t.Fatal("timer fired after flush")
	}
	// Flushed events count for the rate limit.
	if f.Allow(ev(core.EventConnected, "wifi:Home")) || f.Pending() != 0 {
		t.Fatal("connected right after a flushed one must be rate-limited")
	}
}

func TestFilterZeroDebounceAndRate(t *testing.T) {
	p := DefaultPolicy()
	p.Debounce, p.RateLimit = 0, 0
	f, _, _ := setup(t, p)
	for i := 0; i < 3; i++ {
		if !f.Allow(ev(core.EventConnected, "wifi:Home")) {
			t.Fatal("with no debounce, connected passes synchronously")
		}
		if !f.Allow(ev(core.EventDisconnected, "wifi:Home")) {
			t.Fatal("with no rate limit, every disconnect passes")
		}
	}
}

func TestFilterNoOnReady(t *testing.T) {
	clock := newFakeClock()
	f := NewFilterWithClock(DefaultPolicy(), clock)
	f.Allow(ev(core.EventConnected, "wifi:Home"))
	clock.Advance(5 * time.Second) // must not panic without a callback
	f.Allow(ev(core.EventConnected, "wifi:Office"))
	f.Flush()
}

func TestFilterConcurrent(t *testing.T) {
	f := NewFilter(DefaultPolicy())
	f.OnReady(func(core.Event) {})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "wifi:" + string(rune('A'+i%4))
			for j := 0; j < 50; j++ {
				f.Allow(ev(core.EventConnected, key))
				f.Allow(ev(core.EventDisconnected, key))
				f.Allow(ev(core.EventNoInternet, key))
			}
		}(i)
	}
	wg.Wait()
	f.Flush()
}
