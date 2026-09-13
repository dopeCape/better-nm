package nm

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

// newTestClient builds a client over a canned cache; nothing touches a bus.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		log:      slog.Default(),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		objs:     map[dbus.ObjectPath]map[string]props{},
		settings: map[dbus.ObjectPath]settingsDict{},
		devRsn:   map[dbus.ObjectPath]uint32{},
		subs:     map[chan core.Change]struct{}{},
		waiters:  map[dbus.ObjectPath][]chan activeEvent{},
		sysfs: func(name string) (bool, bool) {
			switch name {
			case "wlp4s0", "eno1":
				return true, true
			case "docker0", "vethabc", "tailscale0", "lo":
				return true, false
			}
			return false, false
		},
		username:        "baby",
		uptime:          func() (float64, bool) { return 1000, true },
		activateTimeout: time.Second,
	}
	t.Cleanup(cancel)
	return c
}

const (
	pWifi   dbus.ObjectPath = "/org/freedesktop/NetworkManager/Devices/4"
	pEth    dbus.ObjectPath = "/org/freedesktop/NetworkManager/Devices/2"
	pDocker dbus.ObjectPath = "/org/freedesktop/NetworkManager/Devices/7"
	pTS     dbus.ObjectPath = "/org/freedesktop/NetworkManager/Devices/6"
	pLo     dbus.ObjectPath = "/org/freedesktop/NetworkManager/Devices/1"
	pAP1    dbus.ObjectPath = "/org/freedesktop/NetworkManager/AccessPoint/1"
	pAP2    dbus.ObjectPath = "/org/freedesktop/NetworkManager/AccessPoint/2"
	pAP3    dbus.ObjectPath = "/org/freedesktop/NetworkManager/AccessPoint/3"
	pAPHid  dbus.ObjectPath = "/org/freedesktop/NetworkManager/AccessPoint/4"
	pAC2    dbus.ObjectPath = "/org/freedesktop/NetworkManager/ActiveConnection/2"
	pAC3    dbus.ObjectPath = "/org/freedesktop/NetworkManager/ActiveConnection/3"
	pIP4    dbus.ObjectPath = "/org/freedesktop/NetworkManager/IP4Config/4"
	pIP6    dbus.ObjectPath = "/org/freedesktop/NetworkManager/IP6Config/4"
	pConn2  dbus.ObjectPath = "/org/freedesktop/NetworkManager/Settings/2"
	pConn8  dbus.ObjectPath = "/org/freedesktop/NetworkManager/Settings/8"
	pConn10 dbus.ObjectPath = "/org/freedesktop/NetworkManager/Settings/10"
)

func mv(v any) dbus.Variant { return dbus.MakeVariant(v) }

func seed(c *Client) {
	c.objs[pathNM] = map[string]props{ifaceNM: {
		"State": mv(uint32(70)), "Connectivity": mv(uint32(4)), "Version": mv("1.56.0"),
		"NetworkingEnabled": mv(true), "WirelessEnabled": mv(true), "WirelessHardwareEnabled": mv(true),
		"PrimaryConnection": mv(pAC2), "Devices": mv([]dbus.ObjectPath{pLo, pEth, pWifi, pTS, pDocker}),
	}}
	dev := func(name string, typ, state uint32, ac, ip4, ip6 dbus.ObjectPath, managed bool) map[string]props {
		return map[string]props{ifaceDevice: {
			"Interface": mv(name), "DeviceType": mv(typ), "State": mv(state), "ActiveConnection": mv(ac),
			"Ip4Config": mv(ip4), "Ip6Config": mv(ip6), "Managed": mv(managed), "Driver": mv("drv"), "HwAddress": mv("AA:BB"),
		}}
	}
	c.objs[pLo] = dev("lo", devTypeLoopback, devStateActivated, "/", "/", "/", true)
	c.objs[pEth] = dev("eno1", devTypeEthernet, devStateUnavailable, "/", "/", "/", true)
	c.objs[pEth][ifaceWired] = props{"Speed": mv(uint32(1000)), "Carrier": mv(false)}
	c.objs[pWifi] = dev("wlp4s0", devTypeWifi, devStateActivated, pAC2, pIP4, pIP6, true)
	c.objs[pWifi][ifaceWireless] = props{
		"Bitrate": mv(uint32(54000)), "ActiveAccessPoint": mv(pAP1),
		"AccessPoints": mv([]dbus.ObjectPath{pAP1, pAP2, pAP3, pAPHid}),
	}
	c.objs[pTS] = dev("tailscale0", devTypeTun, devStateActivated, pAC3, "/", "/", true)
	c.objs[pDocker] = dev("docker0", devTypeBridge, devStateUnmanaged, "/", "/", "/", false)
	ap := func(ssid string, str byte, freq, flags, wpa, rsn uint32, bssid string) map[string]props {
		return map[string]props{ifaceAP: {
			"Ssid": mv([]byte(ssid)), "Strength": mv(str), "Frequency": mv(freq), "Flags": mv(flags),
			"WpaFlags": mv(wpa), "RsnFlags": mv(rsn), "HwAddress": mv(bssid), "LastSeen": mv(int32(990)),
		}}
	}
	c.objs[pAP1] = ap("Home", 71, 5805, 1, 0, 392, "24:0B:88:43:B9:2D")
	c.objs[pAP2] = ap("Home", 90, 2437, 1, 0, 392, "24:0B:88:43:B9:2E")
	c.objs[pAP3] = ap("Cafe", 40, 5180, 0, 0, apSecKeyMgmtOWE, "00:11:22:33:44:55")
	c.objs[pAPHid] = ap("", 10, 2412, 1, 0, 392, "00:00:00:00:00:01")
	c.objs[pAC2] = map[string]props{ifaceActive: {
		"Uuid": mv("uuid-home"), "Id": mv("Home"), "Type": mv(typeWifi), "State": mv(activeActivated), "StateFlags": mv(uint32(92)),
		"Default": mv(true), "Default6": mv(false), "Vpn": mv(false), "Devices": mv([]dbus.ObjectPath{pWifi}),
		"Ip4Config": mv(pIP4), "Ip6Config": mv(pIP6),
	}}
	c.objs[pAC3] = map[string]props{ifaceActive: {
		"Uuid": mv("uuid-ts"), "Id": mv("tailscale0"), "Type": mv("tun"), "State": mv(activeActivated), "StateFlags": mv(uint32(220)),
		"Default": mv(false), "Default6": mv(false), "Vpn": mv(false), "Devices": mv([]dbus.ObjectPath{pTS}),
	}}
	c.objs[pIP4] = map[string]props{ifaceIP4Config: {
		"AddressData":    mv([]map[string]dbus.Variant{{"address": mv("192.168.1.73"), "prefix": mv(uint32(24))}}),
		"Gateway":        mv("192.168.1.254"),
		"NameserverData": mv([]map[string]dbus.Variant{{"address": mv("192.168.1.254")}}),
	}}
	c.objs[pIP6] = map[string]props{ifaceIP6Config: {
		"AddressData": mv([]map[string]dbus.Variant{{"address": mv("fe80::1"), "prefix": mv(uint32(64))}}),
		"Nameservers": mv([][]byte{mustV6("fd00::53")}),
	}}
	c.objs[pConn2] = map[string]props{ifaceConnection: {"Filename": mv("/etc/NetworkManager/system-connections/Home.nmconnection"), "VersionId": mv(uint64(2))}}
	c.settings[pConn2] = settingsDict{
		"connection":               {"id": mv("Home"), "uuid": mv("uuid-home"), "type": mv(typeWifi), "interface-name": mv("wlp4s0"), "timestamp": mv(uint64(1789320813))},
		"802-11-wireless":          {"ssid": mv([]byte("Home"))},
		"802-11-wireless-security": {"key-mgmt": mv("wpa-psk")},
		"ipv4":                     {"method": mv("auto")},
	}
	c.objs[pConn8] = map[string]props{ifaceConnection: {}}
	c.settings[pConn8] = settingsDict{
		"connection": {"id": mv("tailscale0"), "uuid": mv("uuid-ts"), "type": mv("tun")},
	}
	c.objs[pConn10] = map[string]props{ifaceConnection: {}}
	c.settings[pConn10] = settingsDict{
		"connection":      {"id": mv("Cafe on eth"), "uuid": mv("uuid-cafe"), "type": mv(typeWifi), "interface-name": mv("wlan9")},
		"802-11-wireless": {"ssid": mv([]byte("Cafe"))},
	}
}

func TestDevicesFromCache(t *testing.T) {
	c := newTestClient(t)
	seed(c)
	devs, err := c.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]core.Device{}
	for _, d := range devs {
		by[d.Name] = d
	}
	if len(by) != 5 {
		t.Fatalf("got %d devices: %v", len(by), devs)
	}
	w := by["wlp4s0"]
	if w.Kind != core.DeviceWifi || w.Class != core.ClassPhysical || w.State != core.DeviceConnected || !w.Managed ||
		w.ActiveUUID != "uuid-home" || w.ActiveName != "Home" || w.Speed != 54 || w.Owner != "" {
		t.Fatalf("wifi device %+v", w)
	}
	if len(w.IPv4) != 1 || w.IPv4[0] != "192.168.1.73/24" || w.Gateway4 != "192.168.1.254" ||
		len(w.IPv6) != 1 || w.IPv6[0] != "fe80::1/64" || len(w.DNS) != 2 || w.DNS[0] != "192.168.1.254" || w.DNS[1] != "fd00::53" {
		t.Fatalf("wifi IPs %+v", w)
	}
	ts := by["tailscale0"]
	if ts.State != core.DeviceExternal || ts.Class != core.ClassInfra || ts.Owner != "tailscale" || ts.Kind != core.DeviceTun {
		t.Fatalf("tailscale device %+v", ts)
	}
	dk := by["docker0"]
	if dk.State != core.DeviceUnmanaged || dk.Managed || dk.Owner != "docker" || dk.Class != core.ClassInfra {
		t.Fatalf("docker device %+v", dk)
	}
	if by["lo"].Class != core.ClassLoopback || by["eno1"].State != core.DeviceUnavailable || by["eno1"].Speed != 1000 {
		t.Fatalf("lo/eno1 %+v %+v", by["lo"], by["eno1"])
	}
}

func TestWifiNetworksFromCache(t *testing.T) {
	c := newTestClient(t)
	seed(c)
	nets, err := c.WifiNetworks(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("want Home and Cafe (hidden skipped), got %+v", nets)
	}
	home := nets[0]
	if home.SSID != "Home" || !home.Active || home.Strength != 90 || home.Frequency != 2437 || home.Channel != 6 || home.Band != "2.4" ||
		home.Security != core.SecWPAPSK || len(home.BSSIDs) != 2 || !home.Known || home.ProfileUUID != "uuid-home" || home.Device != "wlp4s0" {
		t.Fatalf("Home %+v", home)
	}
	if home.LastSeen.IsZero() || time.Since(home.LastSeen) < 9*time.Second || time.Since(home.LastSeen) > 11*time.Second {
		t.Fatalf("LastSeen should be ~10s ago, got %v", time.Since(home.LastSeen))
	}
	cafe := nets[1]
	// The only Cafe profile is bound to another interface: still "known" (fallback).
	if cafe.Security != core.SecOWE || cafe.Active || cafe.Channel != 36 || cafe.Band != "5" || !cafe.Known || cafe.ProfileUUID != "uuid-cafe" {
		t.Fatalf("Cafe %+v", cafe)
	}
	if _, err := c.WifiNetworks(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown device: %v", err)
	}
	if nets, err := c.WifiNetworks(context.Background(), "wlp4s0"); err != nil || len(nets) != 2 {
		t.Fatalf("by device: %v %v", nets, err)
	}
}

func TestProfilesAndActiveFromCache(t *testing.T) {
	c := newTestClient(t)
	seed(c)
	ctx := context.Background()
	profs, err := c.Profiles(ctx)
	if err != nil || len(profs) != 3 {
		t.Fatalf("profiles %v %v", profs, err)
	}
	home := profs[0]
	if home.UUID != "uuid-home" || !home.Active || home.Type != core.ProfileWifi || home.SSID != "Home" || home.Security != core.SecWPAPSK ||
		home.VersionID != 2 || home.Filename == "" || home.NMPath != string(pConn2) || home.Timestamp.Unix() != 1789320813 {
		t.Fatalf("home profile %+v", home)
	}
	if profs[2].Active || profs[2].Type != core.ProfileWifi {
		t.Fatalf("cafe profile %+v", profs[2])
	}
	p, err := c.Profile(ctx, "uuid-ts")
	if err != nil || p.Name != "tailscale0" || !p.Active || p.Type != core.ProfileOther {
		t.Fatalf("Profile(uuid-ts) %+v %v", p, err)
	}
	if _, err := c.Profile(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing profile: %v", err)
	}
	acs, err := c.ActiveConnections(ctx)
	if err != nil || len(acs) != 2 {
		t.Fatalf("active %v %v", acs, err)
	}
	if acs[0].ProfileUUID != "uuid-home" || !acs[0].Default4 || acs[0].External || acs[0].State != core.ActiveActivated ||
		len(acs[0].Devices) != 1 || acs[0].Devices[0] != "wlp4s0" || acs[0].Gateway4 != "192.168.1.254" || acs[0].Type != core.ProfileWifi {
		t.Fatalf("ac home %+v", acs[0])
	}
	if !acs[1].External || acs[1].Type != core.ProfileOther {
		t.Fatalf("ac tailscale %+v", acs[1])
	}
	// NetworkKey for the primary via the same view.
	v := c.view()
	prim := v.active(pAC2)
	if key := core.NetworkKey(&prim, v.profiles()); key != "wifi:Home" {
		t.Fatalf("network key %q", key)
	}
}

func TestSignalsUpdateCacheAndWatch(t *testing.T) {
	c := newTestClient(t)
	seed(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if first.Kind != core.ChangeStatus {
		t.Fatalf("first hint must be status, got %+v", first)
	}

	// Device state change with a reason.
	c.handle(&dbus.Signal{Path: pWifi, Name: ifaceDevice + ".StateChanged", Body: []any{devStateFailed, devStateActivated, devReasonNoSecrets}})
	if h := <-ch; h.Kind != core.ChangeDevices || h.Path != string(pWifi) {
		t.Fatalf("hint %+v", h)
	}
	devs, _ := c.Devices(ctx)
	for _, d := range devs {
		if d.Name == "wlp4s0" && d.State != core.DeviceFailed {
			t.Fatalf("state not updated: %+v", d)
		}
	}

	// Standard PropertiesChanged on the manager.
	c.handle(&dbus.Signal{Path: pathNM, Name: ifaceProperties + ".PropertiesChanged", Body: []any{
		ifaceNM, map[string]dbus.Variant{"WirelessEnabled": mv(false)}, []string{"Version"},
	}})
	if h := <-ch; h.Kind != core.ChangeStatus {
		t.Fatalf("hint %+v", h)
	}
	nm := c.propsOf(pathNM, ifaceNM)
	if on, _ := vBool(nm, "WirelessEnabled"); on {
		t.Fatal("WirelessEnabled not updated")
	}
	if _, ok := nm["Version"]; ok {
		t.Fatal("invalidated property not dropped")
	}

	// InterfacesAdded for a new AP, then AccessPointRemoved for it.
	newAP := dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/9")
	c.handle(&dbus.Signal{Path: pathRoot, Name: ifaceObjectManager + ".InterfacesAdded", Body: []any{
		newAP, map[string]map[string]dbus.Variant{ifaceAP: {"Ssid": mv([]byte("New")), "Strength": mv(byte(5)), "Frequency": mv(uint32(2412))}},
	}})
	if h := <-ch; h.Kind != core.ChangeWifi {
		t.Fatalf("hint %+v", h)
	}
	if c.propsOf(newAP, ifaceAP) == nil {
		t.Fatal("AP not cached")
	}
	c.handle(&dbus.Signal{Path: pWifi, Name: ifaceWireless + ".AccessPointRemoved", Body: []any{newAP}})
	<-ch
	if c.propsOf(newAP, ifaceAP) != nil {
		t.Fatal("AP not removed")
	}

	// InterfacesRemoved drops an object and hints per interface.
	c.handle(&dbus.Signal{Path: pathRoot, Name: ifaceObjectManager + ".InterfacesRemoved", Body: []any{pAC3, []string{ifaceActive}}})
	if h := <-ch; h.Kind != core.ChangeActive {
		t.Fatalf("hint %+v", h)
	}
	if acs, _ := c.ActiveConnections(ctx); len(acs) != 1 {
		t.Fatalf("active connection not removed: %v", acs)
	}

	// Legacy NM-style / mock PropertiesChanged is ignored gracefully (no panic, no hint).
	c.handle(&dbus.Signal{Path: pathNM, Name: ifaceNM + ".PropertiesChanged", Body: []any{map[string]dbus.Variant{"State": mv(uint32(20))}}})
	// NM.StateChanged updates State and hints status.
	c.handle(&dbus.Signal{Path: pathNM, Name: ifaceNM + ".StateChanged", Body: []any{uint32(20)}})
	if h := <-ch; h.Kind != core.ChangeStatus {
		t.Fatalf("hint %+v", h)
	}
	if vU32(c.propsOf(pathNM, ifaceNM), "State") != 20 {
		t.Fatal("NM State not updated")
	}
	// CheckPermissions and VpnStateChanged.
	c.handle(&dbus.Signal{Path: pathNM, Name: ifaceNM + ".CheckPermissions"})
	if h := <-ch; h.Kind != core.ChangeStatus {
		t.Fatalf("hint %+v", h)
	}
	c.handle(&dbus.Signal{Path: pAC2, Name: ifaceVPN + ".VpnStateChanged", Body: []any{uint32(5), uint32(0)}})
	if h := <-ch; h.Kind != core.ChangeVPN {
		t.Fatalf("hint %+v", h)
	}

	// Watch ends with ctx.
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("watch channel not closed after ctx cancel")
		}
	}
}

func TestWatchDropsWhenFull(t *testing.T) {
	c := newTestClient(t)
	seed(c)
	ch, err := c.Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < watchBuffer*3; i++ {
			c.emit(core.ChangeWifi, pWifi) // must never block
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emit blocked on a slow consumer")
	}
	if n := len(ch); n != watchBuffer {
		t.Fatalf("buffer holds %d, want %d", n, watchBuffer)
	}
}

func TestWaitActiveViaSignals(t *testing.T) {
	c := newTestClient(t)
	seed(c)
	ac := dbus.ObjectPath("/org/freedesktop/NetworkManager/ActiveConnection/9")
	c.objs[ac] = map[string]props{ifaceActive: {"State": mv(activeActivating), "Devices": mv([]dbus.ObjectPath{pWifi})}}

	// Success path: waiter registered, then signals arrive.
	errc := make(chan error, 1)
	go func() { errc <- c.waitLoop(context.Background(), "op", ac) }()
	waitForWaiter(t, c, ac)
	c.handle(&dbus.Signal{Path: ac, Name: ifaceActive + ".StateChanged", Body: []any{activeActivating, activeReasonNone}})
	c.handle(&dbus.Signal{Path: ac, Name: ifaceActive + ".StateChanged", Body: []any{activeActivated, activeReasonNone}})
	if err := <-errc; err != nil {
		t.Fatalf("activated must be nil, got %v", err)
	}

	// Wrong password: device reason NO_SECRETS then active DEACTIVATED reason 9.
	go func() { errc <- c.waitLoop(context.Background(), "connect wifi X", ac) }()
	waitForWaiter(t, c, ac)
	c.handle(&dbus.Signal{Path: pWifi, Name: ifaceDevice + ".StateChanged", Body: []any{devStateFailed, devStateNeedAuth, devReasonNoSecrets}})
	c.handle(&dbus.Signal{Path: ac, Name: ifaceActive + ".StateChanged", Body: []any{activeDeactivated, activeReasonNoSecrets}})
	err := <-errc
	if !errors.Is(err, ErrAuthFailed) || err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("wrong password must be ErrAuthFailed with a hint, got %v", err)
	}

	// Supplicant disconnect (reason 8) with a generic active reason.
	go func() { errc <- c.waitLoop(context.Background(), "connect wifi X", ac) }()
	waitForWaiter(t, c, ac)
	c.handle(&dbus.Signal{Path: pWifi, Name: ifaceDevice + ".StateChanged", Body: []any{devStateDisconnected, devStateConfig, devReasonSupplicantDisconnect}})
	c.handle(&dbus.Signal{Path: ac, Name: ifaceActive + ".StateChanged", Body: []any{activeDeactivated, activeReasonUnknown}})
	if err := <-errc; !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("supplicant disconnect must be ErrAuthFailed, got %v", err)
	}

	// Timeout.
	c.activateTimeout = 50 * time.Millisecond
	c.mu.Lock()
	delete(c.devRsn, pWifi)
	c.mu.Unlock()
	if err := c.waitLoop(context.Background(), "op", ac); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	// Caller cancellation.
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	c.activateTimeout = time.Second
	if err := c.waitLoop(cctx, "op", ac); err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("cancel: %v", err)
	}
	if len(c.waiters) != 0 {
		t.Fatalf("waiters leaked: %v", c.waiters)
	}
}

func waitForWaiter(t *testing.T, c *Client, p dbus.ObjectPath) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.subMu.Lock()
		n := len(c.waiters[p])
		c.subMu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("waiter never registered")
}
