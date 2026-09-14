package fake

import (
	"context"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// SecretBroker is an in-memory core.SecretBroker that tests (and `bnmd
// --fake`) drive: Raise publishes a request through the same handler the NM
// agent uses and hands back a channel that receives the answer, or is closed
// when the request is cancelled or expires.
type SecretBroker struct {
	mu       sync.Mutex
	pending  map[string]*fakeSecret
	order    []string
	needed   func(core.SecretRequest)
	resolved func(id string, outcome core.SecretOutcome)
	seq      int
	// Timeout is each request's lifetime; zero means two minutes.
	Timeout time.Duration
}

type fakeSecret struct {
	req     core.SecretRequest
	ch      chan core.SecretAnswer
	outcome core.SecretOutcome
	done    bool
	timer   *time.Timer
}

// NewSecretBroker returns an empty broker.
func NewSecretBroker() *SecretBroker {
	return &SecretBroker{pending: map[string]*fakeSecret{}}
}

// SetSecretHandler installs the daemon's secret-needed callback.
func (b *SecretBroker) SetSecretHandler(f func(core.SecretRequest)) {
	b.mu.Lock()
	b.needed = f
	b.mu.Unlock()
}

// SetSecretResolvedHandler installs the daemon's secret-resolved callback.
func (b *SecretBroker) SetSecretResolvedHandler(f func(string, core.SecretOutcome)) {
	b.mu.Lock()
	b.resolved = f
	b.mu.Unlock()
}

// Raise registers req (filling in ID, CreatedAt and ExpiresAt when empty),
// publishes it and returns the channel the answer arrives on. The channel is
// closed without a value when the request is cancelled or times out.
func (b *SecretBroker) Raise(req core.SecretRequest) <-chan core.SecretAnswer {
	b.mu.Lock()
	if req.ID == "" {
		b.seq++
		var buf [8]byte
		buf[7] = byte(b.seq)
		buf[6] = byte(b.seq >> 8)
		req.ID = hex.EncodeToString(buf[:])
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = req.CreatedAt.Add(timeout)
	}
	if len(req.Fields) == 0 {
		req.Fields = []core.SecretField{{Key: "psk", Label: "Wi-Fi password", Secret: true}}
	}
	s := &fakeSecret{req: req, ch: make(chan core.SecretAnswer, 1)}
	id := req.ID
	s.timer = time.AfterFunc(timeout, func() { b.finish(id, core.SecretTimeout, nil) })
	b.pending[id] = s
	b.order = append(b.order, id)
	needed := b.needed
	b.mu.Unlock()
	if needed != nil {
		needed(req)
	}
	return s.ch
}

// finish resolves id; answer nil means cancelled/timed out (channel closed).
func (b *SecretBroker) finish(id string, outcome core.SecretOutcome, answer *core.SecretAnswer) error {
	b.mu.Lock()
	s := b.pending[id]
	if s == nil {
		b.mu.Unlock()
		return core.Errorf(core.KindNotFound, "run `bnm secrets`", "fake: no secret request %s", id)
	}
	if s.done {
		b.mu.Unlock()
		return core.Errorf(core.KindConflict, "", "fake: secret request %s already %s", id, s.outcome)
	}
	s.done = true
	s.outcome = outcome
	s.timer.Stop()
	if answer != nil {
		s.ch <- *answer
	}
	close(s.ch)
	resolved := b.resolved
	b.mu.Unlock()
	if resolved != nil {
		resolved(id, outcome)
	}
	return nil
}

// Pending lists unresolved requests, oldest first.
func (b *SecretBroker) Pending(ctx context.Context) ([]core.SecretRequest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.SecretRequest
	for _, id := range b.order {
		if s := b.pending[id]; s != nil && !s.done {
			out = append(out, s.req)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Answer resolves a request with a.
func (b *SecretBroker) Answer(ctx context.Context, id string, a core.SecretAnswer) error {
	b.mu.Lock()
	s := b.pending[id]
	if s != nil && !s.done {
		got := 0
		for _, f := range s.req.Fields {
			if _, ok := a.Secrets[f.Key]; ok {
				got++
			}
		}
		if got == 0 {
			b.mu.Unlock()
			return core.Errorf(core.KindInvalid, "", "fake: answer carries none of the requested secrets")
		}
	}
	b.mu.Unlock()
	return b.finish(id, core.SecretAnswered, &a)
}

// Cancel resolves a request as cancelled.
func (b *SecretBroker) Cancel(ctx context.Context, id string) error {
	return b.finish(id, core.SecretCancelled, nil)
}

// Outcome reports how id ended, if it has.
func (b *SecretBroker) Outcome(id string) (core.SecretOutcome, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.pending[id]
	if s == nil || !s.done {
		return "", false
	}
	return s.outcome, true
}

var _ core.SecretBroker = (*SecretBroker)(nil)

// Expire resolves id as timed out, as if nobody answered before ExpiresAt.
func (b *SecretBroker) Expire(id string) {
	_ = b.finish(id, core.SecretTimeout, nil)
}
