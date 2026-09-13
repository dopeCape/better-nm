package nmvpn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/vpn/vpntest"
)

const (
	ovpnUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	swanUUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	wgUUID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func seed(t *testing.T) (*vpntest.FakeNM, *Adapter) {
	t.Helper()
	nm := vpntest.New()
	nm.AddProfile(core.Profile{
		UUID: ovpnUUID, Name: "corp-ovpn", Type: core.ProfileVPN, RawType: "vpn",
		VPNServiceType: "org.freedesktop.NetworkManager.openvpn",
		VPNData:        map[string]string{"remote": "vpn.corp.example:1194", "username": "alice", "dev": "tun"},
	})
	nm.AddProfile(core.Profile{
		UUID: swanUUID, Name: "corp-ipsec", Type: core.ProfileVPN, RawType: "vpn",
		VPNServiceType: "org.freedesktop.NetworkManager.strongswan",
	})
	nm.AddProfile(core.Profile{UUID: wgUUID, Name: "wg", Type: core.ProfileWireGuard})
	return nm, NewAdapter(nm, nil)
}

func find(vpns []core.VPN, id string) core.VPN {
	for _, v := range vpns {
		if v.ID == id {
			return v
		}
	}
	return core.VPN{}
}

func TestNormalizeVPNState(t *testing.T) {
	for in, want := range map[string]string{
		"need-auth": "need-auth", "NEED_AUTH": "need-auth", "NM_VPN_CONNECTION_STATE_NEED_AUTH": "need-auth", "NeedAuth": "need-auth",
		"ip-config-get": "ip-config-get", "IP_CONFIG_GET": "ip-config-get", "IpConfigGet": "ip-config-get",
		"activated": "activated", "Failed": "failed", "": "", "connect": "connect",
	} {
		if got := NormalizeVPNState(in); got != want {
			t.Errorf("NormalizeVPNState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapState(t *testing.T) {
	cases := []struct {
		name   string
		ac     core.ActiveConnection
		active bool
		want   core.VPNState
	}{
		{"none", core.ActiveConnection{}, false, core.VPNDisconnected},
		{"need-auth", core.ActiveConnection{State: core.ActiveActivating, VPNState: "need-auth"}, true, core.VPNNeedsAuth},
		{"prepare", core.ActiveConnection{State: core.ActiveActivating, VPNState: "prepare"}, true, core.VPNConnecting},
		{"connect", core.ActiveConnection{State: core.ActiveActivating, VPNState: "connect"}, true, core.VPNConnecting},
		{"ip-config-get", core.ActiveConnection{State: core.ActiveActivating, VPNState: "ip-config-get"}, true, core.VPNConnecting},
		{"activated", core.ActiveConnection{State: core.ActiveActivated, VPNState: "activated"}, true, core.VPNConnected},
		{"failed", core.ActiveConnection{State: core.ActiveDeactivated, VPNState: "failed"}, true, core.VPNError},
		{"disconnected", core.ActiveConnection{State: core.ActiveDeactivated, VPNState: "disconnected"}, true, core.VPNDisconnected},
		{"no vpnstate, activating", core.ActiveConnection{State: core.ActiveActivating}, true, core.VPNConnecting},
		{"no vpnstate, activated", core.ActiveConnection{State: core.ActiveActivated}, true, core.VPNConnected},
		{"unknown vpnstate, deactivating", core.ActiveConnection{State: core.ActiveDeactivating, VPNState: "unknown"}, true, core.VPNDisconnected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := MapState(tc.ac, tc.active)
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAdapterList(t *testing.T) {
	nm, a := seed(t)
	ctx := context.Background()
	if a.Backend() != core.BackendNMVPN {
		t.Errorf("backend = %s", a.Backend())
	}
	vpns, err := a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vpns) != 2 {
		t.Fatalf("got %d VPNs, want 2 (wireguard excluded): %+v", len(vpns), vpns)
	}
	ov := find(vpns, ovpnUUID)
	if ov.Name != "corp-ovpn" || ov.Kind != "OpenVPN" || ov.Backend != core.BackendNMVPN || ov.State != core.VPNDisconnected || !ov.Writable {
		t.Errorf("openvpn = %+v", ov)
	}
	if ov.NMVPN == nil || ov.NMVPN.ServiceType != "org.freedesktop.NetworkManager.openvpn" ||
		ov.NMVPN.Gateway != "vpn.corp.example:1194" || ov.NMVPN.Username != "alice" {
		t.Errorf("openvpn info = %+v", ov.NMVPN)
	}
	sw := find(vpns, swanUUID)
	if sw.Kind != "IPsec (strongSwan)" || sw.NMVPN == nil || sw.NMVPN.Gateway != "" {
		t.Errorf("strongswan = %+v info=%+v", sw, sw.NMVPN)
	}

	// Walk the NM VpnState machine.
	for vs, want := range map[string]core.VPNState{
		"prepare": core.VPNConnecting, "need-auth": core.VPNNeedsAuth, "connect": core.VPNConnecting,
		"ip-config-get": core.VPNConnecting, "activated": core.VPNConnected,
	} {
		st := core.ActiveActivating
		if vs == "activated" {
			st = core.ActiveActivated
		}
		nm.SetActive(core.ActiveConnection{ProfileUUID: ovpnUUID, State: st, VPN: true, VPNState: vs, VPNBanner: "Welcome"})
		vpns, _ = a.List(ctx)
		ov = find(vpns, ovpnUUID)
		if ov.State != want || ov.NMVPN.NMVpnState != vs || ov.NMVPN.Banner != "Welcome" {
			t.Errorf("VpnState %s -> %s (%+v)", vs, ov.State, ov.NMVPN)
		}
	}
	nm.SetActive(core.ActiveConnection{ProfileUUID: ovpnUUID, State: core.ActiveDeactivated, VPN: true, VPNState: "failed", VPNBanner: "auth failed"})
	vpns, _ = a.List(ctx)
	ov = find(vpns, ovpnUUID)
	if ov.State != core.VPNError || !strings.Contains(ov.Error, "auth failed") {
		t.Errorf("failed -> %s %q", ov.State, ov.Error)
	}
}

func TestAdapterListPermissions(t *testing.T) {
	nm, a := seed(t)
	ctx := context.Background()
	nm.SetPermission(PermNetworkControl, core.PermNo)
	vpns, _ := a.List(ctx)
	ov := find(vpns, ovpnUUID)
	if ov.Writable || ov.State != core.VPNNeedsSetup || !strings.Contains(ov.Detail, "network-control: no") {
		t.Errorf("no: %+v", ov)
	}
	nm.SetActive(core.ActiveConnection{ProfileUUID: ovpnUUID, State: core.ActiveActivated, VPN: true, VPNState: "activated"})
	vpns, _ = a.List(ctx)
	ov = find(vpns, ovpnUUID)
	if ov.State != core.VPNConnected || ov.Writable {
		t.Errorf("no+active: %+v", ov)
	}
	nm.Fail = map[string]error{"ActiveConnections": errors.New("dbus down")}
	if _, err := a.List(ctx); err == nil {
		t.Error("List with ActiveConnections error succeeded")
	}
}

func TestAdapterConnectDisconnectImport(t *testing.T) {
	nm, a := seed(t)
	ctx := context.Background()

	if err := a.Connect(ctx, ovpnUUID); err != nil {
		t.Fatal(err)
	}
	vpns, _ := a.List(ctx)
	if v := find(vpns, ovpnUUID); v.State != core.VPNConnected {
		t.Errorf("after Connect: %s", v.State)
	}
	if err := a.Disconnect(ctx, ovpnUUID); err != nil {
		t.Fatal(err)
	}
	vpns, _ = a.List(ctx)
	if v := find(vpns, ovpnUUID); v.State != core.VPNDisconnected {
		t.Errorf("after Disconnect: %s", v.State)
	}
	if err := a.Connect(ctx, wgUUID); err == nil || !strings.Contains(err.Error(), "not a plugin VPN") {
		t.Errorf("Connect on wireguard profile: %v", err)
	}
	if err := a.Disconnect(ctx, "missing"); !errors.Is(err, vpntest.ErrNotFound) {
		t.Errorf("Disconnect unknown: %v", err)
	}

	uuid, err := a.Import(ctx, "org.freedesktop.NetworkManager.openvpn", "/home/me/client.ovpn")
	if err != nil {
		t.Fatal(err)
	}
	p, err := nm.Profile(ctx, uuid)
	if err != nil || p.Type != core.ProfileVPN || p.VPNServiceType != "org.freedesktop.NetworkManager.openvpn" || p.Name != "client.ovpn" {
		t.Errorf("imported = %+v (%v)", p, err)
	}
	var importCall *vpntest.Call
	for _, c := range nm.Calls() {
		if c.Method == "ImportVPN" {
			cc := c
			importCall = &cc
		}
	}
	if importCall == nil || importCall.Args[0] != "openvpn" || importCall.Args[1] != "/home/me/client.ovpn" {
		t.Errorf("ImportVPN call = %+v", importCall)
	}
	if _, err := a.Import(ctx, "", "/x"); err == nil {
		t.Error("empty kind accepted")
	}
	nm.Fail = map[string]error{"ImportVPN": errors.New("nmcli: plugin not installed")}
	if _, err := a.Import(ctx, "sstp", "/x"); err == nil || !strings.Contains(err.Error(), "plugin not installed") {
		t.Errorf("ImportVPN failure: %v", err)
	}
}

func TestAdapterWatch(t *testing.T) {
	nm, a := seed(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := a.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nm.Push(core.Change{Kind: core.ChangeDevices})
	nm.Push(core.Change{Kind: core.ChangeActive, Path: "/ac/9"})
	select {
	case c := <-ch:
		if c != (core.Change{Kind: core.ChangeVPN, Path: "/ac/9"}) {
			t.Errorf("got %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("no change")
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
