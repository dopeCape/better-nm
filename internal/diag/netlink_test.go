package diag

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/dopeCape/better-nm/internal/core"
)

func mac(s string) net.HardwareAddr {
	m, _ := net.ParseMAC(s)
	return m
}

func cidr(s string) *net.IPNet {
	ip, n, _ := net.ParseCIDR(s)
	n.IP = ip
	return n
}

var fakeNow = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

// fakeHost installs a synthetic wlp4s0 (idx 4, 192.168.1.73/24) with a
// default route via 192.168.1.254 and a few neighbours, plus docker0 (idx 8)
// and a policy-routing table 52.
func fakeHost(t *testing.T) {
	t.Helper()
	wl := &netlink.Device{LinkAttrs: netlink.LinkAttrs{Index: 4, Name: "wlp4s0", Flags: net.FlagUp}}
	d0 := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Index: 8, Name: "docker0"}}
	links := []netlink.Link{wl, d0}
	routes := []netlink.Route{
		{LinkIndex: 4, Gw: net.ParseIP("192.168.1.254"), Priority: 600, Protocol: netlink.RouteProtocol(syscall.RTPROT_DHCP), Scope: netlink.SCOPE_UNIVERSE, Table: 254, Family: netlink.FAMILY_V4, Src: net.ParseIP("192.168.1.73")},
		{LinkIndex: 4, Dst: cidr("192.168.1.0/24"), Priority: 600, Protocol: netlink.RouteProtocol(syscall.RTPROT_KERNEL), Scope: netlink.SCOPE_LINK, Table: 254, Family: netlink.FAMILY_V4},
		{LinkIndex: 8, Dst: cidr("172.17.0.0/16"), Protocol: netlink.RouteProtocol(syscall.RTPROT_KERNEL), Scope: netlink.SCOPE_LINK, Table: 254, Family: netlink.FAMILY_V4},
		{LinkIndex: 5, Dst: cidr("100.64.0.0/10"), Table: 52, Protocol: netlink.RouteProtocol(syscall.RTPROT_STATIC), Scope: netlink.SCOPE_UNIVERSE, Family: netlink.FAMILY_V4},
	}
	routes6 := []netlink.Route{
		{LinkIndex: 4, Dst: cidr("fe80::/64"), Priority: 256, Protocol: netlink.RouteProtocol(syscall.RTPROT_KERNEL), Scope: netlink.SCOPE_UNIVERSE, Table: 254, Family: netlink.FAMILY_V6},
		{LinkIndex: 4, Gw: net.ParseIP("fe80::1"), Priority: 1024, Protocol: netlink.RouteProtocol(syscall.RTPROT_RA), Scope: netlink.SCOPE_UNIVERSE, Table: 254, Family: netlink.FAMILY_V6},
	}
	neigh4 := []netlink.Neigh{
		{IP: net.ParseIP("192.168.1.254"), HardwareAddr: mac("24:0b:88:43:b9:20"), State: netlink.NUD_REACHABLE, Confirmed: 500},
		{IP: net.ParseIP("192.168.1.10"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: netlink.NUD_STALE, Confirmed: 6000},
		{IP: net.ParseIP("192.168.1.11"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: netlink.NUD_DELAY},
		{IP: net.ParseIP("192.168.1.12"), HardwareAddr: mac("aa:bb:cc:dd:ee:03"), State: netlink.NUD_PROBE},
		{IP: net.ParseIP("192.168.1.250"), State: netlink.NUD_INCOMPLETE, Updated: 100},
		{IP: net.ParseIP("192.168.1.251"), State: netlink.NUD_FAILED, Updated: 200},
		{IP: net.ParseIP("192.168.1.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:ff"), State: netlink.NUD_PERMANENT},
		{IP: net.ParseIP("224.0.0.251"), State: netlink.NUD_NOARP},
		{IP: nil, State: netlink.NUD_REACHABLE},
	}
	neigh6 := []netlink.Neigh{
		{IP: net.ParseIP("fe80::1"), HardwareAddr: mac("24:0b:88:43:b9:20"), State: netlink.NUD_STALE, Confirmed: 1000},
		{IP: net.ParseIP("ff02::16"), State: netlink.NUD_NOARP},
	}

	oldLBN, oldLL, oldAL, oldNL, oldRLF, oldNow := linkByName, linkList, addrList, neighList, routeListFiltered, timeNow
	linkByName = func(name string) (netlink.Link, error) {
		for _, l := range links {
			if l.Attrs().Name == name {
				return l, nil
			}
		}
		return nil, netlink.LinkNotFoundError{}
	}
	linkList = func() ([]netlink.Link, error) { return links, nil }
	addrList = func(l netlink.Link, _ int) ([]netlink.Addr, error) {
		if l.Attrs().Name == "wlp4s0" {
			return []netlink.Addr{{IPNet: cidr("192.168.1.73/24")}, {IPNet: cidr("fe80::d2d7:48d6:a423:cc14/64")}}, nil
		}
		return []netlink.Addr{{IPNet: cidr("172.17.0.1/16")}}, nil
	}
	neighList = func(idx, family int) ([]netlink.Neigh, error) {
		if idx != 4 {
			return nil, nil
		}
		if family == netlink.FAMILY_V6 {
			return neigh6, nil
		}
		return neigh4, nil
	}
	routeListFiltered = func(family int, filter *netlink.Route, mask uint64) ([]netlink.Route, error) {
		var all []netlink.Route
		switch family {
		case netlink.FAMILY_V4:
			all = routes
		case netlink.FAMILY_V6:
			all = routes6
		default:
			all = append(append([]netlink.Route{}, routes...), routes6...)
		}
		var out []netlink.Route
		for _, r := range all {
			if mask&netlink.RT_FILTER_OIF != 0 && r.LinkIndex != filter.LinkIndex {
				continue
			}
			if mask&netlink.RT_FILTER_TABLE == 0 && r.Table != 254 {
				continue
			}
			out = append(out, r)
		}
		return out, nil
	}
	timeNow = func() time.Time { return fakeNow }
	t.Cleanup(func() {
		linkByName, linkList, addrList, neighList, routeListFiltered, timeNow = oldLBN, oldLL, oldAL, oldNL, oldRLF, oldNow
	})
}

// quietResolvers replaces mDNS and reverse DNS with the given fakes.
func quietResolvers(t *testing.T, mdns map[string]string, rdns map[string]string) {
	t.Helper()
	oldM, oldR := mdnsNames, reverseNames
	mdnsNames = func(context.Context, string) map[string]string { return mdns }
	reverseNames = func(_ context.Context, hosts []core.LANHost) {
		for i := range hosts {
			if hosts[i].Hostname == "" {
				hosts[i].Hostname = rdns[hosts[i].IP]
			}
		}
	}
	t.Cleanup(func() { mdnsNames, reverseNames = oldM, oldR })
}

func TestLANHostsTable(t *testing.T) {
	fakeHost(t)
	quietResolvers(t, map[string]string{"192.168.1.10": "printer.local"}, map[string]string{"192.168.1.254": "router.lan", "192.168.1.10": "ignored"})
	oldSweep := sweepSubnet
	sweepSubnet = func(context.Context, *net.IPNet) error { t.Fatal("sweep must not run"); return nil }
	t.Cleanup(func() { sweepSubnet = oldSweep })

	hosts, err := LANHosts(context.Background(), "wlp4s0", false)
	if err != nil {
		t.Fatal(err)
	}
	byIP := map[string]core.LANHost{}
	for _, h := range hosts {
		byIP[h.IP] = h
		if h.Device != "wlp4s0" {
			t.Errorf("%s device = %q", h.IP, h.Device)
		}
	}
	want := map[string]string{
		"192.168.1.73": "reachable", "192.168.1.254": "reachable", "192.168.1.1": "reachable",
		"192.168.1.10": "stale", "192.168.1.11": "stale", "192.168.1.12": "stale",
		"192.168.1.250": "incomplete", "192.168.1.251": "failed", "fe80::1": "stale",
	}
	if len(hosts) != len(want) {
		t.Errorf("got %d hosts, want %d: %+v", len(hosts), len(want), hosts)
	}
	for ip, st := range want {
		h, ok := byIP[ip]
		if !ok {
			t.Errorf("missing %s", ip)
			continue
		}
		if h.State != st {
			t.Errorf("%s state = %s, want %s", ip, h.State, st)
		}
	}
	if _, ok := byIP["224.0.0.251"]; ok {
		t.Error("NOARP entry leaked")
	}
	if !byIP["192.168.1.73"].Self || byIP["192.168.1.73"].Gateway {
		t.Errorf("self = %+v", byIP["192.168.1.73"])
	}
	if !byIP["192.168.1.254"].Gateway || byIP["192.168.1.254"].MAC != "24:0b:88:43:b9:20" {
		t.Errorf("gateway = %+v", byIP["192.168.1.254"])
	}
	if !byIP["fe80::1"].Gateway {
		t.Errorf("v6 gateway = %+v", byIP["fe80::1"])
	}
	if byIP["192.168.1.10"].Hostname != "printer.local" {
		t.Errorf("mDNS name not applied: %+v", byIP["192.168.1.10"])
	}
	if byIP["192.168.1.254"].Hostname != "router.lan" {
		t.Errorf("rDNS name not applied: %+v", byIP["192.168.1.254"])
	}
	if got := byIP["192.168.1.254"].Seen; !got.Equal(fakeNow.Add(-5 * time.Second)) {
		t.Errorf("seen = %v, want now-5s", got)
	}
	if got := byIP["192.168.1.250"].Seen; !got.Equal(fakeNow.Add(-1 * time.Second)) {
		t.Errorf("incomplete seen = %v, want now-1s", got)
	}
	// sorted: v4 numerically first, then v6
	if hosts[0].IP != "192.168.1.1" || hosts[len(hosts)-1].IP != "fe80::1" {
		t.Errorf("order: %s ... %s", hosts[0].IP, hosts[len(hosts)-1].IP)
	}
}

func TestLANHostsSweepRefused(t *testing.T) {
	fakeHost(t)
	quietResolvers(t, nil, nil)
	oldSweep := sweepSubnet
	var got *net.IPNet
	sweepSubnet = func(_ context.Context, self *net.IPNet) error {
		got = self
		return ErrNeedsPingGroup
	}
	t.Cleanup(func() { sweepSubnet = oldSweep })

	hosts, err := LANHosts(context.Background(), "wlp4s0", true)
	var se *SweepError
	if !errors.As(err, &se) || !errors.Is(err, ErrNeedsPingGroup) {
		t.Fatalf("err = %v, want *SweepError wrapping ErrNeedsPingGroup", err)
	}
	if len(hosts) == 0 {
		t.Fatal("table must still be returned")
	}
	if got == nil || got.IP.String() != "192.168.1.73" {
		t.Errorf("sweep self = %v", got)
	}
	if !strings.Contains(core.HintOf(err), "ping_group_range") {
		t.Errorf("error lacks the hint: %v (hint %q)", err, core.HintOf(err))
	}
}

func TestLANHostsUnknownDevice(t *testing.T) {
	fakeHost(t)
	if _, err := LANHosts(context.Background(), "nope0", false); err == nil {
		t.Fatal("expected error for unknown device")
	}
}

func TestSweepTargets(t *testing.T) {
	tests := []struct {
		self      string
		wantN     int
		first     string
		last      string
		skipsSelf bool
	}{
		{"192.168.1.73/24", 253, "192.168.1.1", "192.168.1.254", true},
		{"10.0.0.1/16", 254, "10.0.0.2", "10.0.0.255", true},
		{"172.19.0.1/16", 254, "172.19.0.2", "172.19.0.255", true},
		{"192.168.1.5/30", 1, "192.168.1.6", "192.168.1.6", true},
		{"192.168.1.5/31", 0, "", "", false},
	}
	for _, tc := range tests {
		got := sweepTargets(cidr(tc.self))
		if len(got) != tc.wantN {
			t.Errorf("%s: %d targets, want %d", tc.self, len(got), tc.wantN)
			continue
		}
		if tc.wantN == 0 {
			continue
		}
		if got[0].String() != tc.first || got[len(got)-1].String() != tc.last {
			t.Errorf("%s: range %s..%s, want %s..%s", tc.self, got[0], got[len(got)-1], tc.first, tc.last)
		}
		self := cidr(tc.self).IP
		for _, ip := range got {
			if ip.Equal(self) {
				t.Errorf("%s: self included", tc.self)
			}
		}
	}
}

func TestRoutes(t *testing.T) {
	fakeHost(t)
	routes, err := Routes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 6 {
		t.Fatalf("got %d routes: %+v", len(routes), routes)
	}
	// sorted by table: 52 first
	if routes[0].Table != 52 || routes[0].Dest != "100.64.0.0/10" || routes[0].Proto != "static" || routes[0].Device != "" {
		t.Errorf("table 52 route = %+v", routes[0])
	}
	var def core.Route
	for _, r := range routes {
		if r.Dest == "default" && r.Family == 4 {
			def = r
		}
	}
	want := core.Route{Dest: "default", Gateway: "192.168.1.254", Device: "wlp4s0", Metric: 600, Proto: "dhcp", Scope: "universe", Table: 254, Family: 4}
	if def != want {
		t.Errorf("default = %+v, want %+v", def, want)
	}
	for _, r := range routes {
		if r.Dest == "fe80::/64" && (r.Family != 6 || r.Proto != "kernel" || r.Device != "wlp4s0") {
			t.Errorf("v6 route = %+v", r)
		}
		if r.Dest == "default" && r.Family == 6 && (r.Gateway != "fe80::1" || r.Proto != "ra") {
			t.Errorf("v6 default = %+v", r)
		}
		if r.Dest == "172.17.0.0/16" && (r.Device != "docker0" || r.Scope != "link") {
			t.Errorf("docker route = %+v", r)
		}
	}
}

func TestNeighState(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{netlink.NUD_REACHABLE, "reachable"},
		{netlink.NUD_PERMANENT, "reachable"},
		{netlink.NUD_STALE, "stale"},
		{netlink.NUD_DELAY, "stale"},
		{netlink.NUD_PROBE, "stale"},
		{netlink.NUD_INCOMPLETE, "incomplete"},
		{netlink.NUD_FAILED, "failed"},
		{netlink.NUD_REACHABLE | netlink.NUD_STALE, "reachable"},
	}
	for _, tc := range tests {
		if got := neighState(tc.in); got != tc.want {
			t.Errorf("neighState(%#x) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
