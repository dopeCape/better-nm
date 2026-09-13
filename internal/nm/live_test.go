//go:build live

package nm

// Live smoke test against this machine's real NetworkManager over the system
// bus. Strictly read-only: it never activates, deactivates, scans or edits.
//
//	go test -tags live -count=1 -run Live ./internal/nm/...

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

func TestLiveReadOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := New(ctx)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skipf("NetworkManager not reachable: %v", err)
		}
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	ch, err := c.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-ch:
		if h.Kind != core.ChangeStatus {
			t.Fatalf("first hint %+v", h)
		}
		t.Logf("watch: initial hint %+v", h)
	case <-time.After(2 * time.Second):
		t.Fatal("Watch delivered no initial hint")
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.NMVersion == "" || st.NMState == "" || st.NMState == "unknown" {
		t.Fatalf("status incomplete: %+v", st)
	}
	t.Logf("status: NM %s state=%s connectivity=%s networking=%v wifi=%v/%v key=%q",
		st.NMVersion, st.NMState, st.Connectivity, st.Networking, st.WifiEnabled, st.WifiHardware, st.NetworkKey)
	if st.Primary != nil {
		t.Logf("primary: %s (%s) on %v state=%s default4=%v ipv4=%v gw=%s dns=%v",
			st.Primary.ProfileName, st.Primary.ProfileUUID, st.Primary.Devices, st.Primary.State, st.Primary.Default4, st.Primary.IPv4, st.Primary.Gateway4, st.Primary.DNS)
		if st.NetworkKey == "" {
			t.Error("a primary connection must yield a network key")
		}
	}

	perms, err := c.Permissions(ctx)
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if len(perms) == 0 {
		t.Fatal("GetPermissions returned nothing")
	}
	t.Logf("permissions: %d actions, network-control=%s wifi.scan=%s modify.own=%s modify.system=%s; auth-needed=%v",
		len(perms), perms[permNetworkControl], perms[permWifiScan], perms[permModifyOwn], perms[permModifySystem], PolkitAuthActions(perms))
	if len(st.Permissions) != len(perms) {
		t.Error("Status.Permissions must mirror Permissions()")
	}

	devs, err := c.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) == 0 {
		t.Fatal("no devices")
	}
	var wifi, lo int
	for _, d := range devs {
		t.Logf("device: %-16s kind=%-9s class=%-8s state=%-12s managed=%-5v owner=%-9q hw=%s drv=%s ipv4=%v gw=%s dns=%v active=%q speed=%d ifindex=%d",
			d.Name, d.Kind, d.Class, d.State, d.Managed, d.Owner, d.HwAddr, d.Driver, d.IPv4, d.Gateway4, d.DNS, d.ActiveName, d.Speed, d.IfIndex)
		if d.Name == "" || d.Kind == "" || d.Class == "" || d.State == "" || d.NMPath == "" {
			t.Errorf("incomplete device %+v", d)
		}
		if d.Kind == core.DeviceWifi {
			wifi++
		}
		if d.Class == core.ClassLoopback {
			lo++
		}
		if d.Class == core.ClassPhysical && d.Owner != "" {
			t.Errorf("physical device %s must not have an owner", d.Name)
		}
		if d.IfIndex == 0 {
			t.Errorf("device %s has no ifindex", d.Name)
		}
	}
	if lo != 1 {
		t.Errorf("expected exactly one loopback device, got %d", lo)
	}

	profs, err := c.Profiles(ctx)
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(profs) == 0 {
		t.Fatal("no profiles visible")
	}
	for _, p := range profs {
		t.Logf("profile: %-24q uuid=%s type=%-9s raw=%-15s iface=%-12s auto=%-5v ssid=%q sec=%q vpn=%q v4=%s v6=%s perms=%v version=%d active=%v file=%s",
			p.Name, p.UUID, p.Type, p.RawType, p.InterfaceName, p.Autoconnect, p.SSID, p.Security, p.VPNServiceType, p.IPv4.Method, p.IPv6.Method, p.Permissions, p.VersionID, p.Active, p.Filename)
		if p.UUID == "" || p.Name == "" || p.RawType == "" || p.NMPath == "" {
			t.Errorf("incomplete profile %+v", p)
		}
		if p.Type == core.ProfileWifi && p.SSID == "" {
			t.Errorf("wifi profile %s has no SSID", p.Name)
		}
		if p.Type == core.ProfileVPN && p.VPNServiceType == "" {
			t.Errorf("vpn profile %s has no service type", p.Name)
		}
		got, err := c.Profile(ctx, p.UUID)
		if err != nil || got.UUID != p.UUID {
			t.Errorf("Profile(%s): %+v %v", p.UUID, got, err)
		}
	}

	acs, err := c.ActiveConnections(ctx)
	if err != nil {
		t.Fatalf("ActiveConnections: %v", err)
	}
	if len(acs) == 0 {
		t.Fatal("no active connections (is the machine online?)")
	}
	for _, a := range acs {
		t.Logf("active: %-24q uuid=%s type=%-9s devices=%v state=%-11s default=%v external=%v vpn=%v/%s ipv4=%v",
			a.ProfileName, a.ProfileUUID, a.Type, a.Devices, a.State, a.Default4, a.External, a.VPN, a.VPNState, a.IPv4)
		if a.Path == "" || a.ProfileUUID == "" || a.State == core.ActiveUnknown {
			t.Errorf("incomplete active connection %+v", a)
		}
	}

	nets, err := c.WifiNetworks(ctx, "")
	if err != nil {
		t.Fatalf("WifiNetworks: %v", err)
	}
	if wifi > 0 && st.WifiEnabled && len(nets) == 0 {
		t.Error("Wi-Fi is on but no networks are visible (was there ever a scan?)")
	}
	for _, n := range nets {
		t.Logf("wifi: %-28q dev=%s strength=%3d freq=%d ch=%d band=%s sec=%-7s bssids=%d known=%-5v active=%-5v seen=%s",
			n.SSID, n.Device, n.Strength, n.Frequency, n.Channel, n.Band, n.Security, len(n.BSSIDs), n.Known, n.Active, n.LastSeen.Format(time.Kitchen))
		if n.SSID == "" || n.Device == "" || n.Security == "" || n.Frequency == 0 || n.Channel == 0 || n.Band == "" || len(n.BSSIDs) == 0 {
			t.Errorf("incomplete network %+v", n)
		}
		if n.Known != (n.ProfileUUID != "") {
			t.Errorf("known/profile mismatch %+v", n)
		}
	}
	if wifi > 0 {
		if _, err := c.WifiNetworks(ctx, "no-such-device"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown device must be ErrNotFound: %v", err)
		}
	}
	if _, err := c.Profile(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown profile must be ErrNotFound: %v", err)
	}

	// Best effort: NM usually emits something (AP strength, statistics, LastSeen)
	// within a few seconds. Log it to show the signal path is wired; never fail.
	select {
	case h, ok := <-ch:
		if ok {
			t.Logf("watch: live hint %+v", h)
		}
	case <-time.After(8 * time.Second):
		t.Log("watch: no live signal within 8s (quiet bus); not a failure")
	}
}
