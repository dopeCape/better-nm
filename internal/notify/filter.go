package notify

import (
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Clock is the time source the Filter uses; tests inject a fake.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is the subset of *time.Timer the Filter needs.
type Timer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Filter applies a Policy to a stream of Events:
//
//   - disabled types and muted Network Keys are dropped;
//   - a connected event is held for Policy.Debounce; if a disconnected event
//     for the same Network Key arrives meanwhile both are dropped, otherwise
//     the held event is handed to the OnReady callback when the window ends;
//   - at most one event per (type, Network Key) passes per Policy.RateLimit.
//
// Allow reports synchronously whether an event may be delivered now; held
// events surface later through OnReady (or Flush). Safe for concurrent use.
type Filter struct {
	mu      sync.Mutex
	policy  Policy
	clock   Clock
	onReady func(core.Event)
	held    map[string]*heldEvent // Network Key -> held connected event
	last    map[string]time.Time  // rateKey -> last delivery
}

type heldEvent struct {
	event core.Event
	timer Timer
}

// NewFilter returns a Filter on the real clock.
func NewFilter(p Policy) *Filter {
	return NewFilterWithClock(p, realClock{})
}

// NewFilterWithClock returns a Filter driven by clock.
func NewFilterWithClock(p Policy, clock Clock) *Filter {
	return &Filter{
		policy: p,
		clock:  clock,
		held:   map[string]*heldEvent{},
		last:   map[string]time.Time{},
	}
}

// OnReady sets the callback that receives held events once their debounce
// window ends without a cancelling disconnect. It is called outside the
// Filter's lock, from a timer goroutine or from Flush.
func (f *Filter) OnReady(fn func(core.Event)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onReady = fn
}

// Allow reports whether e should be delivered now. A connected event that is
// being held returns false; it reaches OnReady later if not cancelled.
func (f *Filter) Allow(e core.Event) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.policy.IsEnabled(e.Type) {
		return false
	}
	if e.Type == core.EventSecretNeeded {
		// A prompt, not a status notice: never held, coalesced or muted.
		return true
	}
	if f.policy.IsMuted(e.NetworkKey) {
		return false
	}
	now := f.clock.Now()
	if f.limited(e, now) {
		return false
	}

	switch e.Type {
	case core.EventConnected:
		if f.policy.Debounce <= 0 {
			f.record(e, now)
			return true
		}
		if _, pending := f.held[e.NetworkKey]; pending {
			return false // coalesce with the one already held
		}
		h := &heldEvent{event: e}
		key := e.NetworkKey
		h.timer = f.clock.AfterFunc(f.policy.Debounce, func() { f.release(key, h) })
		f.held[key] = h
		return false

	case core.EventDisconnected:
		if h, pending := f.held[e.NetworkKey]; pending {
			// A flap: the connect never reached the user, neither does this.
			h.timer.Stop()
			delete(f.held, e.NetworkKey)
			return false
		}
	}
	f.record(e, now)
	return true
}

// Flush delivers every held event immediately (daemon shutdown, tests).
func (f *Filter) Flush() {
	f.mu.Lock()
	var ready []core.Event
	now := f.clock.Now()
	for key, h := range f.held {
		h.timer.Stop()
		delete(f.held, key)
		f.record(h.event, now)
		ready = append(ready, h.event)
	}
	fn := f.onReady
	f.mu.Unlock()
	if fn == nil {
		return
	}
	for _, e := range ready {
		fn(e)
	}
}

// Pending reports how many connected events are currently held.
func (f *Filter) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.held)
}

// release is the debounce timer callback.
func (f *Filter) release(key string, h *heldEvent) {
	f.mu.Lock()
	if f.held[key] != h { // cancelled or flushed meanwhile
		f.mu.Unlock()
		return
	}
	delete(f.held, key)
	f.record(h.event, f.clock.Now())
	fn := f.onReady
	f.mu.Unlock()
	if fn != nil {
		fn(h.event)
	}
}

func rateKey(e core.Event) string { return string(e.Type) + "\x00" + e.NetworkKey }

func (f *Filter) limited(e core.Event, now time.Time) bool {
	if f.policy.RateLimit <= 0 {
		return false
	}
	last, ok := f.last[rateKey(e)]
	return ok && now.Sub(last) < f.policy.RateLimit
}

func (f *Filter) record(e core.Event, now time.Time) {
	f.last[rateKey(e)] = now
}
