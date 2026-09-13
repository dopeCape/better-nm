package wireguard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/vpn/vpntest"
)

func seed(t *testing.T) (*vpntest.FakeNM, *Adapter, string) {
	t.Helper()
	nm := vpntest.New()
	uuid := nm.AddProfile(core.Profile{
		UUID: "11111111-1111-4111-8111-111111111111", Name: "office", Type: core.ProfileWireGuard, RawType: "wireguard",
		InterfaceName: "wg-office",
		IPv4:          core.IPConfig{Method: core.IPManual, Addresses: []string{"10.9.0.2/24"}},
		WireGuard: &core.WireGuardSetting{ListenPort: 51820, Peers: []core.WireGuardPeer{
			{PublicKey: keyB, AllowedIPs: []string{"10.9.0.0/24"}, Endpoint: "vpn.example.com:51820"},
		}},
	})
	nm.AddProfile(core.Profile{UUID: "22222222-2222-4222-8222-222222222222", Name: "Home Wi-Fi", Type: core.ProfileWifi, SSID: "home"})
	nm.AddProfile(core.Profile{UUID: "33333333-3333-4333-8333-333333333333", Name: "corp", Type: core.ProfileVPN, VPNServiceType: "org.freedesktop.NetworkManager.openvpn"})
	return nm, NewAdapter(nm, nil), uuid
}

func TestAdapterList(t *testing.T) {
	nm, a, uuid := seed(t)
	ctx := context.Background()

	if a.Backend() != core.BackendWireGuard {
		t.Errorf("backend = %s", a.Backend())
	}
	vpns, err := a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vpns) != 1 {
		t.Fatalf("got %d VPNs, want only the wireguard profile: %+v", len(vpns), vpns)
	}
	v := vpns[0]
	if v.ID != uuid || v.Name != "office" || v.Kind != "WireGuard" || v.Backend != core.BackendWireGuard {
		t.Errorf("identity = %+v", v)
	}
	if v.State != core.VPNDisconnected || !v.Writable || v.Detail != "" {
		t.Errorf("state = %s writable=%v detail=%q", v.State, v.Writable, v.Detail)
	}
	if v.WireGuard == nil || v.WireGuard.InterfaceName != "wg-office" || v.WireGuard.ListenPort != 51820 ||
		len(v.WireGuard.Peers) != 1 || len(v.WireGuard.Addresses) != 1 || v.WireGuard.Addresses[0] != "10.9.0.2/24" {
		t.Errorf("info = %+v", v.WireGuard)
	}

	// Activating -> connecting; activated -> connected with live addresses.
	nm.SetActive(core.ActiveConnection{ProfileUUID: uuid, State: core.ActiveActivating, Devices: []string{"wg-office"}})
	vpns, _ = a.List(ctx)
	if vpns[0].State != core.VPNConnecting {
		t.Errorf("activating -> %s", vpns[0].State)
	}
	nm.SetActive(core.ActiveConnection{ProfileUUID: uuid, State: core.ActiveActivated, Devices: []string{"wg-office"}, IPv4: []string{"10.9.0.2/24"}, IPv6: []string{"fd00::2/64"}})
	vpns, _ = a.List(ctx)
	if vpns[0].State != core.VPNConnected || len(vpns[0].WireGuard.Addresses) != 2 {
		t.Errorf("activated -> %s addrs=%v", vpns[0].State, vpns[0].WireGuard.Addresses)
	}
	nm.SetActive(core.ActiveConnection{ProfileUUID: uuid, State: core.ActiveDeactivating})
	vpns, _ = a.List(ctx)
	if vpns[0].State != core.VPNDisconnected {
		t.Errorf("deactivating -> %s", vpns[0].State)
	}
}

func TestAdapterListPermissions(t *testing.T) {
	nm, a, uuid := seed(t)
	ctx := context.Background()
	nm.SetPermission(PermNetworkControl, core.PermAuth)
	vpns, _ := a.List(ctx)
	if vpns[0].Writable || vpns[0].State != core.VPNNeedsSetup || !strings.Contains(vpns[0].Detail, "network-control: auth") {
		t.Errorf("auth: %+v", vpns[0])
	}
	// A tunnel that is up stays "connected" even if we could not toggle it.
	nm.SetActive(core.ActiveConnection{ProfileUUID: uuid, State: core.ActiveActivated})
	vpns, _ = a.List(ctx)
	if vpns[0].State != core.VPNConnected || vpns[0].Writable || vpns[0].Detail == "" {
		t.Errorf("auth+active: %+v", vpns[0])
	}
	// Permissions lookup failing is not fatal.
	nm.Fail = map[string]error{"Permissions": errors.New("dbus down")}
	vpns, err := a.List(ctx)
	if err != nil || vpns[0].Writable || !strings.Contains(vpns[0].Detail, "unknown") {
		t.Errorf("perm error: %v %+v", err, vpns[0])
	}
	// Profiles failing is.
	nm.Fail = map[string]error{"Profiles": errors.New("dbus down")}
	if _, err := a.List(ctx); err == nil {
		t.Error("List with Profiles error succeeded")
	}
}

func TestAdapterConnectDisconnect(t *testing.T) {
	nm, a, uuid := seed(t)
	ctx := context.Background()
	nm.ResetCalls()

	if err := a.Connect(ctx, uuid); err != nil {
		t.Fatal(err)
	}
	vpns, _ := a.List(ctx)
	if vpns[0].State != core.VPNConnected {
		t.Errorf("after Connect: %s", vpns[0].State)
	}
	if err := a.Disconnect(ctx, uuid); err != nil {
		t.Fatal(err)
	}
	vpns, _ = a.List(ctx)
	if vpns[0].State != core.VPNDisconnected {
		t.Errorf("after Disconnect: %s", vpns[0].State)
	}
	var seen []string
	for _, c := range nm.Calls() {
		if c.Method == "Activate" || c.Method == "Deactivate" {
			seen = append(seen, c.Method+":"+strings.Join(c.Args, ","))
		}
	}
	if strings.Join(seen, " ") != "Activate:"+uuid+", Deactivate:"+uuid {
		t.Errorf("calls = %v", seen)
	}

	// Wrong type and unknown IDs are refused before touching NM.
	if err := a.Connect(ctx, "33333333-3333-4333-8333-333333333333"); err == nil || !strings.Contains(err.Error(), "not wireguard") {
		t.Errorf("Connect on vpn profile: %v", err)
	}
	if err := a.Connect(ctx, "nope"); !errors.Is(err, vpntest.ErrNotFound) {
		t.Errorf("Connect unknown: %v", err)
	}
	nm.Fail = map[string]error{"Activate": errors.New("polkit said no")}
	if err := a.Connect(ctx, uuid); err == nil || !strings.Contains(err.Error(), "polkit said no") {
		t.Errorf("Activate failure not wrapped: %v", err)
	}
}

func TestAdapterImport(t *testing.T) {
	nm, a, _ := seed(t)
	ctx := context.Background()

	uuid, spec, err := a.Import(ctx, "mullvad-us.conf", strings.NewReader(mullvadConf))
	if err != nil {
		t.Fatal(err)
	}
	if uuid == "" || spec.Name != "mullvad-us" || len(spec.Peers) != 1 {
		t.Errorf("import = %q %+v", uuid, spec)
	}
	p, err := nm.Profile(ctx, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != core.ProfileWireGuard || p.Name != "mullvad-us" || p.WireGuard == nil || len(p.WireGuard.Peers) != 1 {
		t.Errorf("stored profile = %+v", p)
	}
	vpns, _ := a.List(ctx)
	if len(vpns) != 2 {
		t.Errorf("after import: %d VPNs", len(vpns))
	}

	_, _, err = a.Import(ctx, "bad.conf", strings.NewReader("[Interface]\nPrivateKey=nope\n"))
	if !errors.Is(err, ErrInvalidConf) {
		t.Errorf("bad conf: %v", err)
	}
	nm.Fail = map[string]error{"AddWireGuard": errors.New("modify.own denied")}
	if _, _, err := a.Import(ctx, "x.conf", strings.NewReader(mullvadConf)); err == nil {
		t.Error("AddWireGuard failure not surfaced")
	}
}

func TestAdapterWatch(t *testing.T) {
	nm, a, uuid := seed(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := a.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nm.Push(core.Change{Kind: core.ChangeWifi, Path: "ignored"})
	nm.Push(core.Change{Kind: core.ChangeActive, Path: "/ac/1"})
	nm.Push(core.Change{Kind: core.ChangeProfiles, Path: uuid})
	nm.Push(core.Change{Kind: core.ChangeStatus})

	var got []core.Change
	timeout := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case c := <-ch:
			got = append(got, c)
		case <-timeout:
			t.Fatalf("got %v", got)
		}
	}
	if got[0] != (core.Change{Kind: core.ChangeVPN, Path: "/ac/1"}) || got[1] != (core.Change{Kind: core.ChangeVPN, Path: uuid}) {
		t.Errorf("forwarded = %+v", got)
	}
	select {
	case c := <-ch:
		t.Errorf("unexpected %+v", c)
	case <-time.After(30 * time.Millisecond):
	}
	// Activate through NM shows up as a VPN change too.
	if err := a.Connect(ctx, uuid); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-ch:
		if c.Kind != core.ChangeVPN {
			t.Errorf("activate change = %+v", c)
		}
	case <-time.After(time.Second):
		t.Error("no change after Activate")
	}
	cancel()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}

	nm.Fail = map[string]error{"Watch": errors.New("no bus")}
	if _, err := a.Watch(context.Background()); err == nil {
		t.Error("Watch error not surfaced")
	}
}
