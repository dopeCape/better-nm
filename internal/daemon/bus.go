package daemon

import (
	"sync"
	"sync/atomic"

	"github.com/dopeCape/better-nm/internal/core"
)

// StreamItem is one item on the event stream: exactly one of Change or Event is set.
type StreamItem struct {
	Change *core.Change `json:"change,omitempty"`
	Event  *core.Event  `json:"event,omitempty"`
}

// subscriberBuffer is how many items a slow subscriber may lag before it drops.
const subscriberBuffer = 128

type subscriber struct {
	ch      chan StreamItem
	dropped atomic.Int64
}

// bus fans StreamItems out to any number of subscribers without ever blocking
// the publisher: a subscriber that has fallen subscriberBuffer items behind
// loses the newest item (drop-slowest).
type bus struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

func newBus() *bus { return &bus{subs: map[*subscriber]struct{}{}} }

func (b *bus) subscribe() (*subscriber, func()) {
	s := &subscriber{ch: make(chan StreamItem, subscriberBuffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return s, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
			close(s.ch)
		})
	}
}

func (b *bus) publish(item StreamItem) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		select {
		case s.ch <- item:
		default:
			s.dropped.Add(1)
		}
	}
}

func (b *bus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
