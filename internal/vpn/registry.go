// Package vpn merges the VPN backends (Tailscale, NM WireGuard, NM plugin
// VPNs) behind one Registry: one list, one Connect/Disconnect routed by ID,
// one fan-in Watch, plus access to the Tailscale control surface when that
// adapter is present. The registry never fails as a whole because one backend
// is down: a broken adapter becomes an "unavailable" entry in the list.
//
// Tested with stub adapters and vpntest.FakeNM-backed adapters; no D-Bus,
// no tailscaled.
package vpn

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/dopeCape/better-nm/internal/core"
)

// Registry is the daemon's single entry point to every VPN.
type Registry struct {
	adapters []core.VPNAdapter
	byID     map[string]core.VPNBackend // last seen ID -> backend
	mu       sync.Mutex
	log      *slog.Logger
}

// NewRegistry wires adapters in the given order (nil entries are skipped).
func NewRegistry(adapters ...core.VPNAdapter) *Registry {
	r := &Registry{byID: map[string]core.VPNBackend{}, log: slog.Default().With("pkg", "vpn")}
	for _, a := range adapters {
		if a != nil {
			r.adapters = append(r.adapters, a)
		}
	}
	return r
}

// SetLogger replaces the logger (slog.Default() otherwise).
func (r *Registry) SetLogger(l *slog.Logger) {
	if l != nil {
		r.log = l.With("pkg", "vpn")
	}
}

// Adapter returns the adapter for backend, or nil.
func (r *Registry) Adapter(backend core.VPNBackend) core.VPNAdapter {
	for _, a := range r.adapters {
		if a.Backend() == backend {
			return a
		}
	}
	return nil
}

// Tailscale returns the Tailscale control surface when that adapter is
// registered and implements it, else nil.
func (r *Registry) Tailscale() core.TailscaleControl {
	if a := r.Adapter(core.BackendTailscale); a != nil {
		if tc, ok := a.(core.TailscaleControl); ok {
			return tc
		}
	}
	return nil
}

// List merges every adapter. An adapter that errors contributes one entry in
// state unavailable (ID = backend name) instead of failing the call. Order:
// connected first, then by Kind, Name, ID.
func (r *Registry) List(ctx context.Context) ([]core.VPN, error) {
	var out []core.VPN
	owners := map[string]core.VPNBackend{}
	for _, a := range r.adapters {
		vpns, err := a.List(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("vpn: list: %w", ctx.Err())
			}
			r.log.Warn("adapter list failed", "backend", a.Backend(), "err", err)
			out = append(out, unavailable(a.Backend(), err))
			owners[string(a.Backend())] = a.Backend()
			continue
		}
		for _, v := range vpns {
			if v.Backend == "" {
				v.Backend = a.Backend()
			}
			owners[v.ID] = a.Backend()
			out = append(out, v)
		}
	}
	r.mu.Lock()
	for id, b := range owners {
		r.byID[id] = b
	}
	r.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := out[i].State == core.VPNConnected, out[j].State == core.VPNConnected
		if ci != cj {
			return ci
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if !strings.EqualFold(out[i].Name, out[j].Name) {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func unavailable(b core.VPNBackend, err error) core.VPN {
	kind := "VPN"
	switch b {
	case core.BackendTailscale:
		kind = "Tailscale"
	case core.BackendWireGuard:
		kind = "WireGuard"
	}
	return core.VPN{
		ID: string(b), Name: kind, Backend: b, Kind: kind,
		State: core.VPNUnavailable, Error: err.Error(),
		Detail: "backend " + string(b) + " could not be queried",
	}
}

// route finds the adapter owning id: from the last List, else by listing now
// (so Connect works before any List was made).
func (r *Registry) route(ctx context.Context, id string) (core.VPNAdapter, error) {
	r.mu.Lock()
	b, ok := r.byID[id]
	r.mu.Unlock()
	if ok {
		if a := r.Adapter(b); a != nil {
			return a, nil
		}
	}
	if _, err := r.List(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	b, ok = r.byID[id]
	r.mu.Unlock()
	if ok {
		if a := r.Adapter(b); a != nil {
			return a, nil
		}
	}
	return nil, fmt.Errorf("vpn: no VPN with id %q", id)
}

// Connect routes to the adapter owning id.
func (r *Registry) Connect(ctx context.Context, id string) error {
	a, err := r.route(ctx, id)
	if err != nil {
		return err
	}
	return a.Connect(ctx, id)
}

// Disconnect routes to the adapter owning id.
func (r *Registry) Disconnect(ctx context.Context, id string) error {
	a, err := r.route(ctx, id)
	if err != nil {
		return err
	}
	return a.Disconnect(ctx, id)
}

// Watch fans every adapter's Watch into one channel. An adapter whose Watch
// fails is logged and skipped; the channel closes when ctx ends (or when
// every source has closed).
func (r *Registry) Watch(ctx context.Context) (<-chan core.Change, error) {
	out := make(chan core.Change, 32)
	var wg sync.WaitGroup
	started := 0
	for _, a := range r.adapters {
		src, err := a.Watch(ctx)
		if err != nil {
			r.log.Warn("adapter watch failed", "backend", a.Backend(), "err", err)
			continue
		}
		started++
		wg.Add(1)
		go func(src <-chan core.Change) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case c, ok := <-src:
					if !ok {
						return
					}
					if c.Kind == "" {
						c.Kind = core.ChangeVPN
					}
					select {
					case out <- c:
					default:
					}
				}
			}
		}(src)
	}
	if started == 0 && len(r.adapters) > 0 {
		close(out)
		return out, fmt.Errorf("vpn: watch: no adapter could be watched")
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out, nil
}
