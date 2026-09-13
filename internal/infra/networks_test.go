package infra

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
)

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mac(s string) net.HardwareAddr {
	m, _ := net.ParseMAC(s)
	return m
}

// fakeWorld swaps the netlink funcs for a small synthetic host:
//
//	lo, wlp4s0 (physical), docker0 (bridge, no members),
//	br-f1926d4e98a8 (bridge) <- vethA, vethB, tailscale0 (tun).
func fakeWorld(t *testing.T) {
	t.Helper()
	links := []netlink.Link{
		&netlink.Device{LinkAttrs: netlink.LinkAttrs{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback, MTU: 65536}},
		&netlink.Device{LinkAttrs: netlink.LinkAttrs{Index: 4, Name: "wlp4s0", Flags: net.FlagUp, MTU: 1500, HardwareAddr: mac("84:9e:56:22:1f:c3")}},
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Index: 5, Name: "docker0", Flags: net.FlagUp, OperState: netlink.OperDown, MTU: 1500, HardwareAddr: mac("ea:55:b5:eb:03:26")}}, // admin up, no carrier
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Index: 6, Name: "br-f1926d4e98a8", Flags: net.FlagUp, MTU: 1500, HardwareAddr: mac("e6:b5:6b:65:5d:54")}},
		&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Index: 7, Name: "vethA", Flags: net.FlagUp, MasterIndex: 6, NetNsID: 0, MTU: 1500}},
		&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Index: 8, Name: "vethB", Flags: net.FlagUp, MasterIndex: 6, NetNsID: 1, MTU: 1500}},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Index: 9, Name: "tailscale0", Flags: net.FlagUp, MTU: 1280}},
	}
	addrs := map[string][]string{
		"lo":              {"127.0.0.1/8", "::1/128"},
		"wlp4s0":          {"192.168.1.73/24"},
		"docker0":         {"172.17.0.1/16"},
		"br-f1926d4e98a8": {"172.18.0.1/16", "fe80::e4b5:6bff:fe65:5d54/64"},
	}
	neighs := map[int][]netlink.Neigh{
		6: {
			{IP: net.ParseIP("172.18.0.2"), HardwareAddr: mac("02:42:ac:12:00:02"), State: netlink.NUD_REACHABLE},
			{IP: net.ParseIP("172.18.0.3"), HardwareAddr: mac("02:42:ac:12:00:03"), State: netlink.NUD_STALE},
			{IP: net.ParseIP("ff02::16"), State: netlink.NUD_NOARP},
		},
		7: {
			{IP: net.ParseIP("172.18.0.2"), HardwareAddr: mac("02:42:ac:12:00:02"), State: netlink.NUD_REACHABLE}, // dup
		},
	}

	oldLL, oldAL, oldNL, oldSys := linkList, addrList, neighList, sysfsRoot
	linkList = func() ([]netlink.Link, error) { return links, nil }
	addrList = func(l netlink.Link, _ int) ([]netlink.Addr, error) {
		var out []netlink.Addr
		for _, c := range addrs[l.Attrs().Name] {
			ip, n, _ := net.ParseCIDR(c)
			n.IP = ip
			out = append(out, netlink.Addr{IPNet: n})
		}
		return out, nil
	}
	neighList = func(idx, _ int) ([]netlink.Neigh, error) { return neighs[idx], nil }
	sysfsRoot = t.TempDir()
	mustMkdir(t, filepath.Join(sysfsRoot, "wlp4s0", "device"))
	t.Cleanup(func() { linkList, addrList, neighList, sysfsRoot = oldLL, oldAL, oldNL, oldSys })
}

func TestLinks(t *testing.T) {
	fakeWorld(t)
	links, err := Links(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for i, l := range links {
		byName[l.Name] = i
	}
	lo := links[byName["lo"]]
	if lo.Kind != "loopback" || !lo.Up || lo.PeerNetNS != -1 || len(lo.Addresses) != 2 {
		t.Errorf("lo = %+v", lo)
	}
	wl := links[byName["wlp4s0"]]
	if wl.Kind != "device" || wl.HwAddr != "84:9e:56:22:1f:c3" || wl.Addresses[0] != "192.168.1.73/24" || wl.MTU != 1500 {
		t.Errorf("wlp4s0 = %+v", wl)
	}
	va := links[byName["vethA"]]
	if va.Kind != "veth" || va.Master != "br-f1926d4e98a8" || va.PeerNetNS != 0 {
		t.Errorf("vethA = %+v", va)
	}
	if vb := links[byName["vethB"]]; vb.PeerNetNS != 1 {
		t.Errorf("vethB.PeerNetNS = %d", vb.PeerNetNS)
	}
	if ts := links[byName["tailscale0"]]; ts.Kind != "tun" || ts.MTU != 1280 {
		t.Errorf("tailscale0 = %+v", ts)
	}
	if d := links[byName["docker0"]]; d.Up || d.Kind != "bridge" {
		t.Errorf("docker0 = %+v", d)
	}
}

func TestNetworksNetlinkOnly(t *testing.T) {
	fakeWorld(t)
	old := dockerSocket
	dockerSocket = func() string { return filepath.Join(t.TempDir(), "nope.sock") }
	t.Cleanup(func() { dockerSocket = old })

	nets, err := Networks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("got %d networks, want 2: %+v", len(nets), nets)
	}
	d0, br := nets[0], nets[1]
	if d0.Bridge.Name != "docker0" || d0.Owner != "docker" || len(d0.Members) != 0 || d0.Source != "netlink" || d0.Reachable != "not-running" {
		t.Errorf("docker0 = %+v", d0)
	}
	if br.Bridge.Name != "br-f1926d4e98a8" || br.Owner != "docker" || len(br.Members) != 2 {
		t.Errorf("br = %+v", br)
	}
	if len(br.Neighbours) != 2 {
		t.Fatalf("neighbours = %+v", br.Neighbours)
	}
	if n := br.Neighbours[0]; n.IP != "172.18.0.2" || n.MAC != "02:42:ac:12:00:02" || n.State != "reachable" || n.Device != "br-f1926d4e98a8" {
		t.Errorf("neigh0 = %+v", n)
	}
	if n := br.Neighbours[1]; n.IP != "172.18.0.3" || n.State != "stale" {
		t.Errorf("neigh1 = %+v", n)
	}
}

func TestNetworksDockerEnrichment(t *testing.T) {
	fakeWorld(t)
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/networks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
		 {"Name":"bridge","Id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Driver":"bridge","Options":{"com.docker.network.bridge.name":"docker0"}},
		 {"Name":"mosaic-ai_default","Id":"f1926d4e98a8deadbeefdeadbeefdeadbeef","Driver":"bridge","Options":{}},
		 {"Name":"host","Id":"bbbbbbbbbbbbbbbbbbbbbbbb","Driver":"host","Options":{}}
		]`))
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	old := dockerSocket
	dockerSocket = func() string { return sock }
	t.Cleanup(func() { dockerSocket = old })

	nets, err := Networks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("got %d networks", len(nets))
	}
	if nets[0].OwnerName != "bridge" || nets[0].Reachable != "ok" || nets[0].Source != "docker-api" {
		t.Errorf("docker0 = %+v", nets[0])
	}
	if nets[1].OwnerName != "mosaic-ai_default" || nets[1].Reachable != "ok" {
		t.Errorf("br = %+v", nets[1])
	}
}

func TestDockerNeedsGroup(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores socket modes")
	}
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if err := os.Chmod(sock, 0o000); err != nil {
		t.Fatal(err)
	}
	old := dockerSocket
	dockerSocket = func() string { return sock }
	t.Cleanup(func() { dockerSocket = old })

	if _, r := dockerNetworks(context.Background()); r != "needs-group" {
		t.Errorf("reachable = %q, want needs-group", r)
	}
}

func TestDockerBridgeName(t *testing.T) {
	tests := []struct {
		n    dockerNetwork
		want string
	}{
		{dockerNetwork{ID: "f1926d4e98a8deadbeef"}, "br-f1926d4e98a8"},
		{dockerNetwork{ID: "abc", Options: map[string]string{"com.docker.network.bridge.name": "docker0"}}, "docker0"},
		{dockerNetwork{ID: "short"}, ""},
	}
	for _, tc := range tests {
		if got := tc.n.bridgeName(); got != tc.want {
			t.Errorf("bridgeName(%+v) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
