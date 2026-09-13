package fake

import (
	"context"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Store is an in-memory core.Store.
type Store struct {
	mu        sync.Mutex
	samples   []core.Sample
	baselines map[string]core.Baseline // key+"|"+anchor
	speed     []core.SpeedResult
	events    []core.Event
	closed    bool
	// Retention bounds Prune; zero keeps everything.
	Retention time.Duration
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{baselines: map[string]core.Baseline{}}
}

func (s *Store) AddSample(ctx context.Context, smp core.Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, smp)
	return nil
}

func (s *Store) Samples(ctx context.Context, networkKey, anchor string, limit int) ([]core.Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Sample
	for _, x := range s.samples {
		if (networkKey == "" || x.NetworkKey == networkKey) && (anchor == "" || x.Anchor == anchor) {
			out = append(out, x)
		}
	}
	return tail(out, limit), nil
}

func (s *Store) Baselines(ctx context.Context, networkKey string) ([]core.Baseline, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Baseline
	for _, b := range s.baselines {
		if networkKey == "" || b.NetworkKey == networkKey {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *Store) PutBaseline(ctx context.Context, b core.Baseline) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baselines[b.NetworkKey+"|"+b.Anchor] = b
	return nil
}

func (s *Store) DeleteBaselines(ctx context.Context, networkKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, b := range s.baselines {
		if b.NetworkKey == networkKey {
			delete(s.baselines, k)
		}
	}
	return nil
}

func (s *Store) AddSpeedResult(ctx context.Context, r core.SpeedResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.speed = append(s.speed, r)
	return nil
}

func (s *Store) SpeedResults(ctx context.Context, networkKey string, limit int) ([]core.SpeedResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.SpeedResult
	for _, r := range s.speed {
		if networkKey == "" || r.NetworkKey == networkKey {
			out = append(out, r)
		}
	}
	return tail(out, limit), nil
}

func (s *Store) AddEvent(ctx context.Context, e core.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

// Events returns the last limit events, oldest first (0 = all).
func (s *Store) Events(ctx context.Context, limit int) ([]core.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return tail(append([]core.Event(nil), s.events...), limit), nil
}

func (s *Store) Prune(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Retention == 0 {
		return nil
	}
	cut := time.Now().Add(-s.Retention)
	kept := s.samples[:0]
	for _, x := range s.samples {
		if x.Time.After(cut) {
			kept = append(kept, x)
		}
	}
	s.samples = kept
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Closed reports whether Close was called.
func (s *Store) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func tail[T any](in []T, limit int) []T {
	if limit > 0 && len(in) > limit {
		return in[len(in)-limit:]
	}
	return in
}

// Notifier records every event handed to it.
type Notifier struct {
	mu     sync.Mutex
	events []core.Event
	ch     chan core.Event
	// Err, when set, is returned by Notify.
	Err error
}

// NewNotifier returns a recording notifier.
func NewNotifier() *Notifier {
	return &Notifier{ch: make(chan core.Event, 64)}
}

func (n *Notifier) Notify(ctx context.Context, e core.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Err != nil {
		return n.Err
	}
	n.events = append(n.events, e)
	select {
	case n.ch <- e:
	default:
	}
	return nil
}

// Events returns a copy of everything notified so far.
func (n *Notifier) Events() []core.Event {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]core.Event(nil), n.events...)
}

// C delivers notified events to a test as they happen.
func (n *Notifier) C() <-chan core.Event { return n.ch }
