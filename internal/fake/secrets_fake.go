package fake

import (
	"context"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// SecretBroker is an in-memory core.SecretBroker: a test Raises a request the
// way NetworkManager's GetSecrets would, a surface answers or cancels it
// through the API, and the channel Raise returned tells the test which.
// Notify, when set, receives the secret-needed / secret-resolved events the
// real broker emits on the daemon stream (tests wire it to Monitor.Emit).
type SecretBroker struct {
	mu      sync.Mutex
	order   []string
	pending map[string]*raisedSecret
	Notify  func(core.Event)
}

type raisedSecret struct {
	req core.SecretRequest
	ch  chan core.SecretAnswer
}

var _ core.SecretBroker = (*SecretBroker)(nil)

// NewSecretBroker returns an empty broker.
func NewSecretBroker() *SecretBroker {
	return &SecretBroker{pending: map[string]*raisedSecret{}}
}

// Raise registers req and emits secret-needed. The channel delivers the
// answer once one arrives and is closed without a value on cancel.
func (b *SecretBroker) Raise(req core.SecretRequest) <-chan core.SecretAnswer {
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = req.CreatedAt.Add(2 * time.Minute)
	}
	ch := make(chan core.SecretAnswer, 1)
	b.mu.Lock()
	b.pending[req.ID] = &raisedSecret{req: req, ch: ch}
	b.order = append(b.order, req.ID)
	b.mu.Unlock()
	b.notify(core.Event{
		Type: core.EventSecretNeeded, Title: "Password needed for " + req.ConnectionName, Urgency: "normal",
		Data: map[string]string{"request_id": req.ID},
	})
	return ch
}

// Pending lists the open requests in the order they were raised.
func (b *SecretBroker) Pending(ctx context.Context) ([]core.SecretRequest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]core.SecretRequest, 0, len(b.order))
	for _, id := range b.order {
		if r, ok := b.pending[id]; ok {
			out = append(out, r.req)
		}
	}
	return out, nil
}

// Answer resolves id with a and emits secret-resolved (answered).
func (b *SecretBroker) Answer(ctx context.Context, id string, a core.SecretAnswer) error {
	r, err := b.take(id)
	if err != nil {
		return err
	}
	r.ch <- a
	close(r.ch)
	b.notify(resolvedEvent(id, core.SecretAnswered))
	return nil
}

// Cancel resolves id without an answer and emits secret-resolved (cancelled).
func (b *SecretBroker) Cancel(ctx context.Context, id string) error {
	r, err := b.take(id)
	if err != nil {
		return err
	}
	close(r.ch)
	b.notify(resolvedEvent(id, core.SecretCancelled))
	return nil
}

// Expire resolves id as timed out, like the real broker does at ExpiresAt.
func (b *SecretBroker) Expire(id string) {
	r, err := b.take(id)
	if err != nil {
		return
	}
	close(r.ch)
	b.notify(resolvedEvent(id, core.SecretTimeout))
}

func (b *SecretBroker) take(id string) (*raisedSecret, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.pending[id]
	if !ok {
		return nil, core.Errorf(core.KindNotFound, "", "secret request %s is not pending", id)
	}
	delete(b.pending, id)
	for i, o := range b.order {
		if o == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	return r, nil
}

func (b *SecretBroker) notify(e core.Event) {
	b.mu.Lock()
	fn := b.Notify
	b.mu.Unlock()
	if fn != nil {
		if e.Time.IsZero() {
			e.Time = time.Now()
		}
		fn(e)
	}
}

func resolvedEvent(id string, outcome core.SecretOutcome) core.Event {
	return core.Event{
		Type: core.EventSecretResolved, Title: "Password request " + string(outcome), Urgency: "low",
		Data: map[string]string{"request_id": id, "outcome": string(outcome)},
	}
}
