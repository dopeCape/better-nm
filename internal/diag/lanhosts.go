package diag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/vishvananda/netlink"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/dopeCape/better-nm/internal/core"
)

const (
	sweepBudget   = 1500 * time.Millisecond
	mdnsBudget    = 1500 * time.Millisecond
	rdnsBudget    = 300 * time.Millisecond
	rdnsInFlight  = 32
	sweepMaxHosts = 256
)

// Hooks for tests: the sweep and the resolvers are replaced by fakes there.
var (
	sweepSubnet  = icmpSweep
	mdnsNames    = browseMDNS
	reverseNames = reverseDNS
	timeNow      = time.Now
)

// LANHosts lists the neighbours the kernel knows on device, marking the
// default gateway and this host. With sweep it first sends one ICMP echo to
// every address of the device's /24 (or the first 256 of a larger prefix) so
// silent hosts show up in the table; when the ping socket is refused the sweep
// is skipped, the table is still returned, and the error is a *SweepError
// wrapping ErrNeedsPingGroup. Names come from mDNS then reverse DNS, best
// effort.
func LANHosts(ctx context.Context, device string, sweep bool) ([]core.LANHost, error) {
	link, err := linkByName(device)
	if err != nil {
		return nil, fmt.Errorf("diag: device %s: %w", device, err)
	}
	attrs := link.Attrs()
	addrs, err := addrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("diag: addresses of %s: %w", device, err)
	}
	gateways := defaultGateways(attrs.Index)

	// mDNS browse runs while the sweep and the neighbour dump happen.
	mdnsCtx, mdnsCancel := context.WithTimeout(ctx, mdnsBudget)
	defer mdnsCancel()
	mdnsCh := make(chan map[string]string, 1)
	go func() { mdnsCh <- mdnsNames(mdnsCtx, device) }()

	var sweepErr error
	if sweep {
		if v4 := firstIPv4(addrs); v4 != nil {
			sweepErr = sweepSubnet(ctx, v4)
		} else {
			sweepErr = fmt.Errorf("no IPv4 address on %s", device)
		}
	}

	now := timeNow()
	hosts := selfHosts(device, addrs, now)
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		neighs, err := neighList(attrs.Index, family)
		if err != nil {
			return nil, fmt.Errorf("diag: neighbours of %s: %w", device, err)
		}
		for _, n := range neighs {
			if h, ok := neighToHost(n, device, now); ok {
				h.Gateway = gateways[h.IP]
				hosts = append(hosts, h)
			}
		}
	}

	// Names: mDNS first, reverse DNS for whatever is left.
	var names map[string]string
	select {
	case names = <-mdnsCh:
	case <-ctx.Done():
	}
	for i := range hosts {
		if n := names[hosts[i].IP]; n != "" {
			hosts[i].Hostname = n
		}
	}
	if ctx.Err() == nil {
		reverseNames(ctx, hosts)
	}

	sort.Slice(hosts, func(i, j int) bool { return lessIP(hosts[i].IP, hosts[j].IP) })
	if sweepErr != nil {
		return hosts, &SweepError{Err: sweepErr}
	}
	return hosts, nil
}

func firstIPv4(addrs []netlink.Addr) *net.IPNet {
	for _, a := range addrs {
		if a.IPNet != nil && a.IP.To4() != nil {
			return &net.IPNet{IP: a.IP.To4(), Mask: a.Mask}
		}
	}
	return nil
}

// selfHosts adds one entry per non-link-local address of the device.
func selfHosts(device string, addrs []netlink.Addr, now time.Time) []core.LANHost {
	var out []core.LANHost
	for _, a := range addrs {
		if a.IPNet == nil || a.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, core.LANHost{IP: a.IP.String(), State: "reachable", Device: device, Self: true, Seen: now})
	}
	return out
}

// defaultGateways returns the gateway IPs of default routes leaving ifindex.
func defaultGateways(ifindex int) map[string]bool {
	out := map[string]bool{}
	routes, err := routeListFiltered(netlink.FAMILY_ALL, &netlink.Route{LinkIndex: ifindex}, netlink.RT_FILTER_OIF)
	if err != nil {
		return out
	}
	for _, r := range routes {
		if !isDefault(r.Dst) {
			continue
		}
		if r.Gw != nil {
			out[r.Gw.String()] = true
		}
		for _, nh := range r.MultiPath {
			if nh.Gw != nil {
				out[nh.Gw.String()] = true
			}
		}
	}
	return out
}

func isDefault(dst *net.IPNet) bool {
	if dst == nil {
		return true
	}
	ones, _ := dst.Mask.Size()
	return ones == 0
}

func neighToHost(n netlink.Neigh, device string, now time.Time) (core.LANHost, bool) {
	if n.IP == nil || n.State == netlink.NUD_NONE || n.State&netlink.NUD_NOARP != 0 {
		return core.LANHost{}, false
	}
	h := core.LANHost{IP: n.IP.String(), State: neighState(n.State), Device: device}
	if len(n.HardwareAddr) > 0 {
		h.MAC = n.HardwareAddr.String()
	}
	switch h.State {
	case "reachable", "stale":
		h.Seen = ticksAgo(now, n.Confirmed)
	default:
		h.Seen = ticksAgo(now, n.Updated)
	}
	return h, true
}

// neighState maps NUD_* bits to the four states core.LANHost documents.
func neighState(s int) string {
	switch {
	case s&(netlink.NUD_REACHABLE|netlink.NUD_PERMANENT) != 0:
		return "reachable"
	case s&(netlink.NUD_STALE|netlink.NUD_DELAY|netlink.NUD_PROBE) != 0:
		return "stale"
	case s&netlink.NUD_INCOMPLETE != 0:
		return "incomplete"
	case s&netlink.NUD_FAILED != 0:
		return "failed"
	}
	return "stale"
}

// lessIP orders IPv4 before IPv6, then numerically.
func lessIP(a, b string) bool {
	ia, ib := net.ParseIP(a), net.ParseIP(b)
	a4, b4 := ia.To4(), ib.To4()
	switch {
	case a4 != nil && b4 == nil:
		return true
	case a4 == nil && b4 != nil:
		return false
	case a4 != nil:
		return string(a4) < string(b4)
	default:
		return string(ia.To16()) < string(ib.To16())
	}
}

// ---- sweep -----------------------------------------------------------------

// icmpSweep sends one echo to each address of the prefix (first 256 hosts at
// most) from an unprivileged ping socket bound to self, then drains replies
// until the budget ends. Silent hosts still land in the neighbour table as
// INCOMPLETE/FAILED; that is the point.
func icmpSweep(ctx context.Context, self *net.IPNet) error {
	conn, err := icmp.ListenPacket("udp4", self.IP.String())
	if err != nil {
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("%w: %w", ErrNeedsPingGroup, err)
		}
		return fmt.Errorf("open ping socket: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	deadline := timeNow().Add(sweepBudget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: 1, Data: []byte("bnm")}}
	pkt, err := msg.Marshal(nil)
	if err != nil {
		return fmt.Errorf("marshal echo: %w", err)
	}
	for _, ip := range sweepTargets(self) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := conn.WriteTo(pkt, &net.UDPAddr{IP: ip}); err != nil {
			if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				return fmt.Errorf("%w: %w", ErrNeedsPingGroup, err)
			}
			// ENOBUFS and friends: keep going, the table fills anyway.
			continue
		}
	}
	// Drain replies until the budget ends so the kernel completes ARP.
	buf := make([]byte, 1500)
	for {
		if _, _, err := conn.ReadFrom(buf); err != nil {
			break
		}
	}
	return ctx.Err()
}

// sweepTargets is every usable address of the prefix except self, capped at
// sweepMaxHosts; the network address and the /24 broadcast are skipped.
func sweepTargets(self *net.IPNet) []net.IP {
	ones, bits := self.Mask.Size()
	if bits != 32 || ones > 30 {
		return nil
	}
	base := self.IP.Mask(self.Mask).To4()
	size := 1 << (32 - ones)
	limit := size - 1 // exclude broadcast
	if limit > sweepMaxHosts {
		limit = sweepMaxHosts
	}
	var out []net.IP
	for i := 1; i < limit; i++ {
		ip := make(net.IP, 4)
		copy(ip, base)
		v := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
		v += uint32(i)
		ip[0], ip[1], ip[2], ip[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
		if ip.Equal(self.IP) {
			continue
		}
		out = append(out, ip)
	}
	return out
}

// ---- names -----------------------------------------------------------------

// browseMDNS collects hostnames advertised on device for the ctx lifetime.
func browseMDNS(ctx context.Context, device string) map[string]string {
	names := map[string]string{}
	iface, err := net.InterfaceByName(device)
	if err != nil {
		return names
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, svc := range []string{"_workstation._tcp", "_services._dns-sd._udp"} {
		r, err := zeroconf.NewResolver(zeroconf.SelectIfaces([]net.Interface{*iface}))
		if err != nil {
			continue
		}
		entries := make(chan *zeroconf.ServiceEntry, 32)
		if err := r.Browse(ctx, svc, "local.", entries); err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range entries { // closed by zeroconf when ctx ends
				host := strings.TrimSuffix(e.HostName, ".")
				if host == "" {
					continue
				}
				mu.Lock()
				for _, ip := range e.AddrIPv4 {
					names[ip.String()] = host
				}
				for _, ip := range e.AddrIPv6 {
					names[ip.String()] = host
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return names
}

// reverseDNS fills Hostname for hosts that still lack one, at most
// rdnsInFlight lookups at a time, rdnsBudget each.
func reverseDNS(ctx context.Context, hosts []core.LANHost) {
	sem := make(chan struct{}, rdnsInFlight)
	var wg sync.WaitGroup
	for i := range hosts {
		if hosts[i].Hostname != "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(h *core.LANHost) {
			defer wg.Done()
			defer func() { <-sem }()
			lctx, cancel := context.WithTimeout(ctx, rdnsBudget)
			defer cancel()
			names, err := net.DefaultResolver.LookupAddr(lctx, h.IP)
			if err == nil && len(names) > 0 {
				h.Hostname = strings.TrimSuffix(names[0], ".")
			}
		}(&hosts[i])
	}
	wg.Wait()
}
