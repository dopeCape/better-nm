//go:build integration

package nm

// Integration tests against python-dbusmock's NetworkManager template on a
// private session bus. Run with:
//
//	nix shell --impure --expr 'let pkgs = (builtins.getFlake "nixpkgs").legacyPackages.${builtins.currentSystem}; in { py = pkgs.python3.withPackages (p: [p.python-dbusmock]); dbus = pkgs.dbus; }' py dbus \
//	  --command env BNM_INTEGRATION=1 go test -tags integration -count=1 ./internal/nm/...
//
// (`nix shell nixpkgs#python3 nixpkgs#python3Packages.python-dbusmock` does not
// put the module on sys.path; withPackages does. `nix develop` works too.)
// Without BNM_INTEGRATION, dbus-daemon, or an importable dbusmock the tests skip.
// Set BNM_DBUSMOCK_PYTHON to pick a specific interpreter.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

const mockIface = "org.freedesktop.DBus.Mock"

type mockBus struct {
	addr string
	conn *dbus.Conn // the client's connection
	ctl  *dbus.Conn // a second connection for driving the mock
}

// startMock launches dbus-daemon and dbusmock, returning once the NM name is owned.
func startMock(t *testing.T) *mockBus {
	t.Helper()
	if os.Getenv("BNM_INTEGRATION") == "" {
		t.Skip("set BNM_INTEGRATION=1 to run dbusmock-backed tests")
	}
	py := os.Getenv("BNM_DBUSMOCK_PYTHON")
	if py == "" {
		py = "python3"
	}
	if out, err := exec.Command(py, "-c", "import dbusmock, dbus").CombinedOutput(); err != nil {
		t.Skipf("python-dbusmock not importable via %s: %v: %s", py, err, strings.TrimSpace(string(out)))
	}
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not on PATH")
	}

	daemon := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--print-address=1")
	stdout, err := daemon.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	daemon.Stderr = os.Stderr
	if err := daemon.Start(); err != nil {
		t.Fatalf("dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _, _ = daemon.Process.Wait() })
	addr, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon printed no address: %v", err)
	}
	addr = strings.TrimSpace(addr)

	mock := exec.Command(py, "-m", "dbusmock", "--session", "--template", "networkmanager",
		"--parameters", `{"Version": "1.56.0"}`)
	mock.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+addr)
	mock.Stderr = os.Stderr
	if err := mock.Start(); err != nil {
		t.Fatalf("dbusmock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Process.Kill(); _, _ = mock.Process.Wait() })

	connect := func() *dbus.Conn {
		c, err := dbus.Connect(addr, dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()))
		if err != nil {
			t.Fatalf("connect %s: %v", addr, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	ctl := connect()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var has bool
		if err := ctl.BusObject().Call(ifaceDBus+".NameHasOwner", 0, busName).Store(&has); err == nil && has {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dbusmock never claimed org.freedesktop.NetworkManager")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &mockBus{addr: addr, conn: connect(), ctl: ctl}
}

// mockCall invokes an org.freedesktop.DBus.Mock helper on the manager object.
func (m *mockBus) mockCall(t *testing.T, method string, args ...any) []any {
	t.Helper()
	call := m.ctl.Object(busName, pathNM).Call(mockIface+"."+method, 0, args...)
	if call.Err != nil {
		t.Fatalf("mock %s%v: %v", method, args, call.Err)
	}
	return call.Body
}

func pathOf(t *testing.T, body []any) dbus.ObjectPath {
	t.Helper()
	if len(body) == 0 {
		t.Fatal("empty reply")
	}
	switch v := body[0].(type) {
	case string:
		return dbus.ObjectPath(v)
	case dbus.ObjectPath:
		return v
	}
	t.Fatalf("unexpected reply %T", body[0])
	return ""
}

// seedMock builds: wlan0 (disconnected) with APs "Cafe" (open) and "Home"
// (WPA-PSK, two BSSIDs), eth0 (activated), and a saved profile for Home.
func seedMock(t *testing.T, m *mockBus) (wlan, eth, homeConn dbus.ObjectPath) {
	t.Helper()
	wlan = pathOf(t, m.mockCall(t, "AddWiFiDevice", "dev_wlan0", "wlan0", int32(devStateDisconnected)))
	eth = pathOf(t, m.mockCall(t, "AddEthernetDevice", "dev_eth0", "eth0", int32(devStateActivated)))
	m.mockCall(t, "AddAccessPoint", string(wlan), "ap_cafe", "Cafe", "00:23:F8:7E:12:BA", uint32(2), uint32(2437), uint32(54), byte(45), uint32(0))
	// The mock's AddWiFiConnection only understands a bare KEY_MGMT_PSK security value.
	m.mockCall(t, "AddAccessPoint", string(wlan), "ap_home1", "Home", "00:23:F8:7E:12:BB", uint32(2), uint32(5180), uint32(300), byte(70), uint32(apSecKeyMgmtPSK))
	m.mockCall(t, "AddAccessPoint", string(wlan), "ap_home2", "Home", "00:23:F8:7E:12:BC", uint32(2), uint32(2412), uint32(54), byte(88), uint32(apSecKeyMgmtPSK))
	homeConn = pathOf(t, m.mockCall(t, "AddWiFiConnection", string(wlan), "home", "Home", "wpa-psk"))
	return wlan, eth, homeConn
}

func newIntegrationClient(t *testing.T, m *mockBus) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, err := New(ctx, WithConn(m.conn))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.activateTimeout = 5 * time.Second
	c.sysfs = func(string) (bool, bool) { return false, false }
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// eventually polls until f returns true.
func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestIntegrationSnapshotReads(t *testing.T) {
	m := startMock(t)
	wlan, _, _ := seedMock(t, m)
	c := newIntegrationClient(t, m)
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.NMVersion != "1.56.0" || st.NMState != "connected-global" || st.Connectivity != core.ConnFull || !st.Networking || !st.WifiEnabled {
		t.Fatalf("status %+v", st)
	}
	if st.Permissions == nil {
		t.Fatal("permissions map must be non-nil even when the mock returns {}")
	}

	devs, err := c.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 {
		t.Fatalf("devices %+v", devs)
	}
	var w, e core.Device
	for _, d := range devs {
		switch d.Name {
		case "wlan0":
			w = d
		case "eth0":
			e = d
		}
	}
	if w.Kind != core.DeviceWifi || w.State != core.DeviceDisconnected || w.Class != core.ClassPhysical || w.NMPath != string(wlan) || w.Driver != "dbusmock" || !w.Managed {
		t.Fatalf("wlan0 %+v", w)
	}
	if e.Kind != core.DeviceEthernet || e.State != core.DeviceConnected || e.HwAddr != "78:DD:08:D2:3D:43" {
		t.Fatalf("eth0 %+v", e)
	}

	nets, err := c.WifiNetworks(ctx, "wlan0")
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("networks %+v", nets)
	}
	home, cafe := nets[0], nets[1]
	if home.SSID != "Home" || home.Strength != 88 || home.Frequency != 2412 || home.Channel != 1 || home.Band != "2.4" ||
		home.Security != core.SecWPAPSK || len(home.BSSIDs) != 2 || !home.Known || home.ProfileUUID == "" || home.Active {
		t.Fatalf("Home %+v", home)
	}
	if cafe.SSID != "Cafe" || cafe.Security != core.SecOpen || cafe.Known || cafe.Channel != 6 {
		t.Fatalf("Cafe %+v", cafe)
	}

	profs, err := c.Profiles(ctx)
	if err != nil || len(profs) != 1 {
		t.Fatalf("profiles %+v %v", profs, err)
	}
	p := profs[0]
	if p.Name != "Home" || p.Type != core.ProfileWifi || p.SSID != "Home" || p.Security != core.SecWPAPSK || p.Active || p.UUID == "" ||
		p.IPv4.Method != core.IPAuto || p.Timestamp.IsZero() {
		t.Fatalf("home profile %+v", p)
	}
	if got, err := c.Profile(ctx, p.UUID); err != nil || got.UUID != p.UUID {
		t.Fatalf("Profile: %+v %v", got, err)
	}
	if acs, err := c.ActiveConnections(ctx); err != nil || len(acs) != 0 {
		t.Fatalf("active %+v %v", acs, err)
	}
	if err := c.Scan(ctx, "wlan0"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := c.Scan(ctx, ""); err != nil {
		t.Fatalf("scan default device: %v", err)
	}
	if err := c.Scan(ctx, "eth0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scan on ethernet must be not found: %v", err)
	}
}

func TestIntegrationActivateDeactivateForget(t *testing.T) {
	m := startMock(t)
	wlan, _, _ := seedMock(t, m)
	c := newIntegrationClient(t, m)
	ctx := context.Background()

	profs, _ := c.Profiles(ctx)
	uuid := profs[0].UUID

	if err := c.Activate(ctx, uuid, "wlan0"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	eventually(t, "active connection in cache", func() bool {
		acs, _ := c.ActiveConnections(ctx)
		return len(acs) == 1 && acs[0].ProfileUUID == uuid && acs[0].State == core.ActiveActivated
	})
	eventually(t, "device activated", func() bool {
		devs, _ := c.Devices(ctx)
		for _, d := range devs {
			if d.Name == "wlan0" {
				return d.State == core.DeviceConnected && d.ActiveUUID == uuid
			}
		}
		return false
	})
	acs, _ := c.ActiveConnections(ctx)
	if len(acs[0].Devices) != 1 || acs[0].Devices[0] != "wlan0" || acs[0].Type != core.ProfileWifi {
		t.Fatalf("active %+v", acs[0])
	}
	if p, _ := c.Profile(ctx, uuid); !p.Active {
		t.Fatalf("profile should be active: %+v", p)
	}
	if st, _ := c.Status(ctx); st.Primary != nil {
		t.Logf("mock has no PrimaryConnection; Primary=%v", st.Primary)
	}

	if err := c.Deactivate(ctx, uuid); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	eventually(t, "active connection gone", func() bool {
		acs, _ := c.ActiveConnections(ctx)
		return len(acs) == 0
	})
	if err := c.Deactivate(ctx, uuid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deactivating an inactive profile must be ErrNotFound: %v", err)
	}
	if err := c.Activate(ctx, "00000000-0000-0000-0000-000000000000", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown uuid: %v", err)
	}
	if err := c.Activate(ctx, uuid, "nosuchdev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown device: %v", err)
	}
	if err := c.DisconnectDevice(ctx, "wlan0"); err == nil {
		t.Log("mock accepted Device.Disconnect (unexpected but harmless)")
	} else if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("mock has no Disconnect; want ErrUnsupported, got %v", err)
	}
	_ = wlan

	if err := c.Forget(ctx, uuid); err != nil {
		t.Fatalf("forget: %v", err)
	}
	eventually(t, "profile removed", func() bool {
		profs, _ := c.Profiles(ctx)
		return len(profs) == 0
	})
	if _, err := c.Profile(ctx, uuid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten profile must be ErrNotFound: %v", err)
	}
	nets, _ := c.WifiNetworks(ctx, "")
	for _, n := range nets {
		if n.Known {
			t.Fatalf("no network should be known after forget: %+v", n)
		}
	}
}

func TestIntegrationConnectWifiNewAndSaved(t *testing.T) {
	m := startMock(t)
	seedMock(t, m)
	c := newIntegrationClient(t, m)
	ctx := context.Background()

	// New open network: AddAndActivateConnection (mock lacks the *2 variant).
	if err := c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: "Cafe"}); err != nil {
		t.Fatalf("connect new: %v", err)
	}
	eventually(t, "Cafe active", func() bool {
		nets, _ := c.WifiNetworks(ctx, "wlan0")
		for _, n := range nets {
			if n.SSID == "Cafe" {
				return n.Active && n.Known
			}
		}
		return false
	})
	// Saved network: ActivateConnection path.
	if err := c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: "Home", Device: "wlan0"}); err != nil {
		t.Fatalf("connect saved: %v", err)
	}
	if err := c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: ""}); err == nil {
		t.Fatal("empty SSID must fail")
	}
	if err := c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: "Nope", Username: "u"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("EAP request must be ErrUnsupported: %v", err)
	}
	if err := c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: "Cafe", Device: "eth0"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-wifi device must be ErrNotFound: %v", err)
	}
}

func TestIntegrationSettingsEdits(t *testing.T) {
	m := startMock(t)
	seedMock(t, m)
	c := newIntegrationClient(t, m)
	ctx := context.Background()
	profs, _ := c.Profiles(ctx)
	uuid := profs[0].UUID

	// SetAutoconnect (Update fallback on the mock).
	if err := c.SetAutoconnect(ctx, uuid, false); err != nil {
		t.Fatalf("autoconnect: %v", err)
	}
	eventually(t, "autoconnect=false", func() bool {
		p, _ := c.Profile(ctx, uuid)
		return !p.Autoconnect
	})

	// UpdateIPConfig round trip through the mock's stored dict.
	v4 := &core.IPConfig{Method: core.IPManual, Addresses: []string{"10.9.8.7/24"}, Gateway: "10.9.8.1", DNS: []string{"9.9.9.9"}, DNSSearch: []string{"lan"}, IgnoreAutoDNS: true}
	v6 := &core.IPConfig{Method: core.IPDisabled}
	if err := c.UpdateIPConfig(ctx, uuid, v4, v6); err != nil {
		t.Fatalf("update ip: %v", err)
	}
	eventually(t, "ip config stored", func() bool {
		p, _ := c.Profile(ctx, uuid)
		return p.IPv4.Method == core.IPManual && len(p.IPv4.Addresses) == 1 && p.IPv4.Addresses[0] == "10.9.8.7/24" &&
			p.IPv4.Gateway == "10.9.8.1" && len(p.IPv4.DNS) == 1 && p.IPv4.DNS[0] == "9.9.9.9" && p.IPv4.IgnoreAutoDNS &&
			p.IPv6.Method == core.IPDisabled
	})
	// Check what the mock actually holds: signatures survived the wire.
	var raw settingsDict
	if err := m.ctl.Object(busName, dbus.ObjectPath(profs[0].NMPath)).Call(ifaceConnection+".GetSettings", 0).Store(&raw); err != nil {
		t.Fatal(err)
	}
	if raw["ipv4"]["dns"].Signature().String() != "au" || raw["ipv4"]["address-data"].Signature().String() != "aa{sv}" || raw["ipv6"]["dns"].Signature().String() != "aay" {
		t.Fatalf("wire signatures: dns=%s address-data=%s dns6=%s", raw["ipv4"]["dns"].Signature(), raw["ipv4"]["address-data"].Signature(), raw["ipv6"]["dns"].Signature())
	}
	if err := c.UpdateIPConfig(ctx, uuid, nil, nil); err != nil {
		t.Fatalf("nil/nil must be a no-op: %v", err)
	}
	if err := c.UpdateIPConfig(ctx, uuid, &core.IPConfig{Addresses: []string{"bad"}}, nil); err == nil {
		t.Fatal("bad address must fail before touching NM")
	}

	// SetProfilePermissions.
	if err := c.SetProfilePermissions(ctx, uuid, true); err != nil {
		t.Fatalf("permissions: %v", err)
	}
	eventually(t, "permissions set", func() bool {
		p, _ := c.Profile(ctx, uuid)
		return len(p.Permissions) == 1 && strings.HasPrefix(p.Permissions[0], "user:") && strings.HasSuffix(p.Permissions[0], ":")
	})
	if err := c.SetProfilePermissions(ctx, uuid, false); err != nil {
		t.Fatalf("permissions off: %v", err)
	}
	eventually(t, "permissions cleared", func() bool {
		p, _ := c.Profile(ctx, uuid)
		return len(p.Permissions) == 0
	})

	// AddWireGuard: the mock stores any dict (AddConnection fallback); no device appears.
	wgUUID, err := c.AddWireGuard(ctx, core.WireGuardSpec{
		Name: "wg-test", InterfaceName: "wg-test", PrivateKey: "cHJpdmF0ZQ==", Addresses: []string{"10.66.0.2/32"},
		Peers: []core.WireGuardPeer{{PublicKey: "cHVibGlj", Endpoint: "vpn.example:51820", AllowedIPs: []string{"0.0.0.0/0"}}},
	})
	if err != nil {
		t.Fatalf("add wireguard: %v", err)
	}
	eventually(t, "wireguard profile visible", func() bool {
		p, err := c.Profile(ctx, wgUUID)
		return err == nil && p.Type == core.ProfileWireGuard && p.InterfaceName == "wg-test" && p.IPv4.Method == core.IPManual && p.IPv6.Method == core.IPDisabled
	})
	if _, err := c.AddWireGuard(ctx, core.WireGuardSpec{Name: "x"}); err == nil {
		t.Fatal("incomplete spec must fail")
	}

	// SetWifiEnabled goes through Properties.Set on the mock.
	if err := c.SetWifiEnabled(ctx, false); err != nil {
		t.Fatalf("wifi off: %v", err)
	}
	eventually(t, "wifi disabled in status", func() bool {
		st, _ := c.Status(ctx)
		return !st.WifiEnabled
	})
	if err := c.SetWifiEnabled(ctx, true); err != nil {
		t.Fatalf("wifi on: %v", err)
	}

	// ImportVPN with a fake nmcli that prints NM's success line.
	fake := t.TempDir() + "/nmcli"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"Connection 'tejas' ("+uuid+") successfully added.\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.nmcli = fake
	got, err := c.ImportVPN(ctx, "openvpn", "/tmp/whatever.ovpn")
	if err != nil || got != uuid {
		t.Fatalf("import: %q %v", got, err)
	}
	eventually(t, "imported profile scoped to user", func() bool {
		p, _ := c.Profile(ctx, uuid)
		return len(p.Permissions) == 1
	})
	if _, err := c.ImportVPN(ctx, "wireguard", "/x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("non-openvpn import: %v", err)
	}
	if _, err := c.ImportVPN(ctx, "open vpn; rm", "/x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bad kind: %v", err)
	}
	failing := t.TempDir() + "/nmcli"
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho 'Error: failed to import: not authorized' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.nmcli = failing
	if _, err := c.ImportVPN(ctx, "openvpn", "/x"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("nmcli auth failure: %v", err)
	}
}

func TestIntegrationWatchAndSignals(t *testing.T) {
	m := startMock(t)
	wlan, _, _ := seedMock(t, m)
	c := newIntegrationClient(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := c.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first := <-ch; first.Kind != core.ChangeStatus {
		t.Fatalf("first hint %+v", first)
	}
	waitKind := func(kind core.ChangeKind) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case h, ok := <-ch:
				if !ok {
					t.Fatal("watch closed")
				}
				if h.Kind == kind {
					return
				}
			case <-deadline:
				t.Fatalf("no %s hint", kind)
			}
		}
	}

	// New AP -> wifi hint and cache update.
	m.mockCall(t, "AddAccessPoint", string(wlan), "ap_new", "Newbie", "00:23:F8:7E:12:CC", uint32(2), uint32(5500), uint32(300), byte(30), uint32(apSecKeyMgmtSAE))
	waitKind(core.ChangeWifi)
	eventually(t, "Newbie visible", func() bool {
		nets, _ := c.WifiNetworks(ctx, "")
		for _, n := range nets {
			if n.SSID == "Newbie" && n.Security == core.SecSAE && n.Channel == 100 {
				return true
			}
		}
		return false
	})
	m.mockCall(t, "RemoveAccessPoint", string(wlan), "/org/freedesktop/NetworkManager/AccessPoint/ap_new")
	waitKind(core.ChangeWifi)
	eventually(t, "Newbie gone", func() bool {
		nets, _ := c.WifiNetworks(ctx, "")
		for _, n := range nets {
			if n.SSID == "Newbie" {
				return false
			}
		}
		return true
	})

	// New device -> devices hint.
	m.mockCall(t, "AddEthernetDevice", "dev_eth1", "eth1", int32(devStateUnavailable))
	waitKind(core.ChangeDevices)
	eventually(t, "eth1 visible", func() bool {
		devs, _ := c.Devices(ctx)
		return len(devs) == 3
	})

	// Global state / connectivity via the mock's setters -> status hints.
	m.mockCall(t, "SetGlobalConnectionState", uint32(20))
	waitKind(core.ChangeStatus)
	eventually(t, "state disconnected", func() bool {
		st, _ := c.Status(ctx)
		return st.NMState == "disconnected"
	})
	m.mockCall(t, "SetConnectivity", uint32(1))
	waitKind(core.ChangeStatus)
	eventually(t, "connectivity none", func() bool {
		st, _ := c.Status(ctx)
		return st.Connectivity == core.ConnNone
	})

	// Device state via SetDeviceDisconnected -> Device.StateChanged.
	m.mockCall(t, "SetDeviceActive", string(wlan), "/org/freedesktop/NetworkManager/ActiveConnection/x")
	waitKind(core.ChangeDevices)
	eventually(t, "wlan0 connected", func() bool {
		devs, _ := c.Devices(ctx)
		for _, d := range devs {
			if d.Name == "wlan0" {
				return d.State == core.DeviceConnected
			}
		}
		return false
	})

	// Profile added through Settings.AddConnection -> profiles hint.
	settings := settingsDict{
		"connection": {"id": dbus.MakeVariant("Wired 9"), "type": dbus.MakeVariant(typeEthernet), "uuid": dbus.MakeVariant("12345678-1234-4123-8123-123456789abc")},
		"ipv4":       {"method": dbus.MakeVariant("auto")},
	}
	var newPath dbus.ObjectPath
	if err := m.ctl.Object(busName, pathSettings).Call(ifaceSettings+".AddConnection", 0, settings).Store(&newPath); err != nil {
		t.Fatal(err)
	}
	waitKind(core.ChangeProfiles)
	eventually(t, "new profile cached", func() bool {
		p, err := c.Profile(ctx, "12345678-1234-4123-8123-123456789abc")
		return err == nil && p.Type == core.ProfileEthernet && p.NMPath == string(newPath)
	})

	// Close the client: Watch channel closes.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("watch not closed on Close")
		}
	}
}

func TestIntegrationNewFailsWithoutNM(t *testing.T) {
	if os.Getenv("BNM_INTEGRATION") == "" {
		t.Skip("set BNM_INTEGRATION=1")
	}
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not on PATH")
	}
	daemon := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--print-address=1")
	stdout, _ := daemon.StdoutPipe()
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _, _ = daemon.Process.Wait() })
	addr, _ := bufio.NewReader(stdout).ReadString('\n')
	conn, err := dbus.Connect(strings.TrimSpace(addr), dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = New(ctx, WithConn(conn))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty bus must be ErrUnavailable, got %v", err)
	}
	fmt.Println("New without NM:", err)
}
