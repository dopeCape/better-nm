package fake

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Diag returns canned diagnostics for the seeded world.
type Diag struct {
	// Err, when set, is returned by every method.
	Err error
}

// NewDiag returns a Diag.
func NewDiag() *Diag { return &Diag{} }

func (d *Diag) LANHosts(ctx context.Context, device string, sweep bool) ([]core.LANHost, error) {
	if d.Err != nil {
		return nil, d.Err
	}
	if device == "" {
		device = WifiDevice
	}
	hosts := []core.LANHost{
		{IP: "192.168.1.1", MAC: "10:00:00:00:00:01", Hostname: "router.lan", State: "reachable", Device: device, Gateway: true, Seen: time.Now()},
		{IP: "192.168.1.42", MAC: "aa:bb:cc:dd:ee:01", Hostname: "laptop", State: "reachable", Device: device, Self: true, Seen: time.Now()},
		{IP: "192.168.1.50", MAC: "50:00:00:00:00:01", Hostname: "printer.lan", State: "stale", Device: device, Seen: time.Now().Add(-10 * time.Minute)},
	}
	if sweep {
		hosts = append(hosts, core.LANHost{IP: "192.168.1.77", MAC: "77:00:00:00:00:01", State: "reachable", Device: device, Seen: time.Now()})
	}
	return hosts, nil
}

func (d *Diag) ListeningPorts(ctx context.Context) ([]core.ListeningPort, error) {
	if d.Err != nil {
		return nil, d.Err
	}
	return []core.ListeningPort{
		{Proto: "tcp", Addr: "0.0.0.0", Port: 22, PID: 811, Process: "sshd", User: "root", UID: 0},
		{Proto: "tcp", Addr: "127.0.0.1", Port: 631, PID: 900, Process: "cupsd", User: "root", UID: 0},
		{Proto: "udp", Addr: "0.0.0.0", Port: 5353, PID: 700, Process: "avahi-daemon", User: "avahi", UID: 70},
	}, nil
}

func (d *Diag) Routes(ctx context.Context) ([]core.Route, error) {
	if d.Err != nil {
		return nil, d.Err
	}
	return []core.Route{
		{Dest: "default", Gateway: "192.168.1.1", Device: WifiDevice, Metric: 600, Proto: "dhcp", Family: 4},
		{Dest: "192.168.1.0/24", Device: WifiDevice, Metric: 600, Proto: "kernel", Scope: "link", Family: 4},
		{Dest: "fe80::/64", Device: WifiDevice, Metric: 1024, Proto: "kernel", Family: 6},
	}, nil
}

func (d *Diag) DNSLookup(ctx context.Context, name, server, qtype string) (core.DNSAnswer, error) {
	if d.Err != nil {
		return core.DNSAnswer{}, d.Err
	}
	if name == "" {
		return core.DNSAnswer{}, core.Errorf(core.KindInvalid, "", "diag: name is required")
	}
	if qtype == "" {
		qtype = "A"
	}
	if server == "" {
		server = "192.168.1.1"
	}
	a := core.DNSAnswer{Name: name, Server: server, Type: strings.ToUpper(qtype), Duration: 8 * time.Millisecond}
	switch a.Type {
	case "A":
		a.Answers = []string{"93.184.216.34"}
	case "AAAA":
		a.Answers = []string{"2606:2800:220:1:248:1893:25c8:1946"}
	default:
		a.Answers = []string{}
	}
	return a, nil
}

func (d *Diag) PublicIP(ctx context.Context) (core.PublicIP, error) {
	if d.Err != nil {
		return core.PublicIP{}, d.Err
	}
	return core.PublicIP{IP: "203.0.113.7", Colo: "FRA", Location: "DE", Warp: "off", Via: "cloudflare-trace"}, nil
}

func (d *Diag) Infra(ctx context.Context) ([]core.InfraNetwork, error) {
	if d.Err != nil {
		return nil, d.Err
	}
	return []core.InfraNetwork{{
		Bridge:  core.Link{Name: "docker0", IfIndex: 5, Kind: "bridge", Up: true, Addresses: []string{"172.17.0.1/16"}, MTU: 1500},
		Owner:   "docker",
		Members: []core.Link{{Name: "veth1a2b3c", IfIndex: 7, Kind: "veth", Master: "docker0", Up: true, PeerNetNS: 0, MTU: 1500}},
		Source:  "netlink",
	}}, nil
}

// WireGuardImporter stands in for internal/vpn/wireguard's Import: it reads
// a wg-quick conf, requires a PrivateKey line and creates a profile in nm.
type WireGuardImporter struct {
	NM *NM
}

// Import parses the minimum of a wg-quick file and adds a WireGuard profile.
func (w *WireGuardImporter) Import(ctx context.Context, name string, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	spec := core.WireGuardSpec{Name: name, InterfaceName: name}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "privatekey":
			spec.PrivateKey = v
		case "address":
			for _, a := range strings.Split(v, ",") {
				spec.Addresses = append(spec.Addresses, strings.TrimSpace(a))
			}
		case "dns":
			spec.DNS = append(spec.DNS, v)
		case "publickey":
			spec.Peers = append(spec.Peers, core.WireGuardPeer{PublicKey: v})
		case "endpoint":
			if len(spec.Peers) > 0 {
				spec.Peers[len(spec.Peers)-1].Endpoint = v
			}
		case "allowedips":
			if len(spec.Peers) > 0 {
				spec.Peers[len(spec.Peers)-1].AllowedIPs = append(spec.Peers[len(spec.Peers)-1].AllowedIPs, v)
			}
		}
	}
	if spec.PrivateKey == "" {
		return "", core.Errorf(core.KindInvalid, "", "wireguard: no PrivateKey in %s", name)
	}
	return w.NM.AddWireGuard(ctx, spec)
}
