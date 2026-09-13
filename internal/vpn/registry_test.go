package vpn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/vpn/nmvpn"
	"github.com/dopeCape/better-nm/internal/vpn/vpntest"
	"github.com/dopeCape/better-nm/internal/vpn/wireguard"
)

// stub is a scripted core.VPNAdapter; stubTS also implements TailscaleControl.
type stub struct {
	backend  core.VPNBackend
	vpns     []core.VPN
	listErr  error
	watchErr error
	changes  chan core.Change
	calls    []string
}

func (s *stub) Backend() core.VPNBackend { return s.backend }
func (s *stub) List(context.Context) ([]core.VPN, error) {
	return append([]core.VPN(nil), s.vpns...), s.listErr
}
func (s *stub) Connect(_ context.Context, id string) error {
	s.calls = append(s.calls, "connect:"+id)
	return nil
}
func (s *stub) Disconnect(_ context.Context, id string) error {
	s.calls = append(s.calls, "disconnect:"+id)
	return nil
}
func (s *stub) Watch(ctx context.Context) (<-chan core.Change, error) {
	if s.watchErr != nil {
		return nil, s.watchErr
	}
	out := make(chan core.Change, 8)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-s.changes:
				out <- c
			}
		}
	}()
	return out, nil
}

type stubTS struct{ stub }

func (s *stubTS) SetExitNode(context.Context, string, bool) error { return nil }
func (s *stubTS) UseExitNode(context.Context, bool) error         { return nil }
func (s *stubTS) SetAcceptDNS(context.Context, bool) error        { return nil }
func (s *stubTS) Login(context.Context) (string, error)           { return "https://login.example", nil }
func (s *stubTS) Logout(context.Context) error                    { return nil }

func TestRegistryListMergeAndSort(t *testing.T) {
	ts := &stubTS{stub{backend: core.BackendTailscale, vpns: []core.VPN{
		{ID: "tailscale", Name: "Tailscale", Kind: "Tailscale", State: core.VPNDisconnected},
	}, changes: make(chan core.Change, 1)}}
	wg := &stub{backend: core.BackendWireGuard, vpns: []core.VPN{
		{ID: "wg-b", Name: "beta", Kind: "WireGuard", State: core.VPNDisconnected},
		{ID: "wg-a", Name: "Alpha", Kind: "WireGuard", State: core.VPNDisconnected},
		{ID: "wg-c", Name: "gamma", Kind: "WireGuard", State: core.VPNConnected},
	}, changes: make(chan core.Change, 1)}
	nmv := &stub{backend: core.BackendNMVPN, listErr: errors.New("dbus gone"), changes: make(chan core.Change, 1)}

	r := NewRegistry(ts, nil, wg, nmv)
	vpns, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, v := range vpns {
		order = append(order, v.ID)
	}
	want := "wg-c tailscale nm-vpn wg-a wg-b"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
	// The failed adapter became an unavailable entry, Backend filled in.
	for _, v := range vpns {
		if v.ID == "nm-vpn" {
			if v.State != core.VPNUnavailable || v.Backend != core.BackendNMVPN || !strings.Contains(v.Error, "dbus gone") {
				t.Errorf("unavailable entry = %+v", v)
			}
		}
		if v.Backend == "" {
			t.Errorf("entry %s has no backend", v.ID)
		}
	}

	if r.Adapter(core.BackendWireGuard) != wg || r.Adapter(core.BackendNMVPN) != nmv || r.Adapter("nope") != nil {
		t.Error("Adapter lookup")
	}
	if r.Tailscale() == nil {
		t.Error("Tailscale() nil with a control-capable adapter")
	}
	if NewRegistry(wg).Tailscale() != nil {
		t.Error("Tailscale() non-nil without the adapter")
	}
	if NewRegistry(&stub{backend: core.BackendTailscale}).Tailscale() != nil {
		t.Error("Tailscale() non-nil for an adapter without the control surface")
	}
}

func TestRegistryRouting(t *testing.T) {
	ts := &stubTS{stub{backend: core.BackendTailscale, vpns: []core.VPN{{ID: "tailscale", Kind: "Tailscale"}}}}
	wg := &stub{backend: core.BackendWireGuard, vpns: []core.VPN{{ID: "wg-a", Kind: "WireGuard"}}}
	r := NewRegistry(ts, wg)
	ctx := context.Background()

	// Before any List: routes by discovering.
	if err := r.Connect(ctx, "wg-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Disconnect(ctx, "tailscale"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(wg.calls, ",") != "connect:wg-a" || strings.Join(ts.calls, ",") != "disconnect:tailscale" {
		t.Errorf("calls: wg=%v ts=%v", wg.calls, ts.calls)
	}
	if err := r.Connect(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown id: %v", err)
	}
	// Cached routing survives an adapter that later fails to list.
	wg.listErr = errors.New("down")
	if err := r.Disconnect(ctx, "wg-a"); err != nil {
		t.Errorf("cached route: %v", err)
	}
}

func TestRegistryWatchFanIn(t *testing.T) {
	ts := &stubTS{stub{backend: core.BackendTailscale, changes: make(chan core.Change, 4)}}
	wg := &stub{backend: core.BackendWireGuard, changes: make(chan core.Change, 4)}
	bad := &stub{backend: core.BackendNMVPN, watchErr: errors.New("no bus")}
	r := NewRegistry(ts, wg, bad)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := r.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ts.changes <- core.Change{Kind: core.ChangeVPN, Path: "tailscale"}
	wg.changes <- core.Change{Path: "wg-a"} // kind defaulted
	seen := map[string]core.ChangeKind{}
	timeout := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case c := <-ch:
			seen[c.Path] = c.Kind
		case <-timeout:
			t.Fatalf("seen %v", seen)
		}
	}
	if seen["tailscale"] != core.ChangeVPN || seen["wg-a"] != core.ChangeVPN {
		t.Errorf("seen = %v", seen)
	}
	cancel()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fan-in channel not closed")
	}

	// Every adapter failing to watch is an error; no adapters is fine.
	if _, err := NewRegistry(bad).Watch(context.Background()); err == nil {
		t.Error("all-failed Watch returned nil error")
	}
	ch, err = NewRegistry().Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("empty registry produced a change")
		}
	case <-time.After(time.Second):
		t.Error("empty registry channel stays open")
	}
}

// End-to-end over FakeNM with the real NM-backed adapters.
func TestRegistryWithNMAdapters(t *testing.T) {
	nm := vpntest.New()
	nm.AddProfile(core.Profile{UUID: "wg-1", Name: "office", Type: core.ProfileWireGuard, InterfaceName: "wg0"})
	nm.AddProfile(core.Profile{UUID: "ovpn-1", Name: "corp", Type: core.ProfileVPN, VPNServiceType: "org.freedesktop.NetworkManager.openvpn"})
	nm.AddProfile(core.Profile{UUID: "wifi-1", Name: "home", Type: core.ProfileWifi})
	r := NewRegistry(wireguard.NewAdapter(nm, nil), nmvpn.NewAdapter(nm, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := r.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vpns, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vpns) != 2 || vpns[0].Kind != "OpenVPN" || vpns[1].Kind != "WireGuard" {
		t.Fatalf("list = %+v", vpns)
	}
	if err := r.Connect(ctx, "wg-1"); err != nil {
		t.Fatal(err)
	}
	// Both NM-backed adapters forward the same NM hint: expect at least one.
	select {
	case c := <-ch:
		if c.Kind != core.ChangeVPN {
			t.Errorf("change = %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("no change after Connect")
	}
	vpns, _ = r.List(ctx)
	if vpns[0].ID != "wg-1" || vpns[0].State != core.VPNConnected {
		t.Errorf("connected first: %+v", vpns[0])
	}
	if err := r.Connect(ctx, "ovpn-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Disconnect(ctx, "wg-1"); err != nil {
		t.Fatal(err)
	}
	vpns, _ = r.List(ctx)
	if vpns[0].ID != "ovpn-1" || vpns[0].State != core.VPNConnected || vpns[1].State != core.VPNDisconnected {
		t.Errorf("after swap: %+v", vpns)
	}
	if err := r.Connect(ctx, "wifi-1"); err == nil {
		t.Error("wifi profile routed as a VPN")
	}
	if r.Tailscale() != nil {
		t.Error("Tailscale() without adapter")
	}
}
