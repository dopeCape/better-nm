// Package infra owns the read-only view of the kernel's link graph: every
// network link with its kind, master and addresses, the physical/infra/loopback
// classification with a runtime owner heuristic, and the "infra networks" view
// (a bridge plus its member links and the neighbours seen on it).
//
// Everything is read through rtnetlink dumps and sysfs, which need no
// privileges. When the Docker socket is connectable the docker network name is
// filled in over plain HTTP; nothing here ever changes network state.
//
// Tested with table tests for Classify and the docker matching, fakes for the
// netlink calls, an httptest server on a unix socket for the docker
// enrichment, and a `//go:build live` test that prints this machine's links
// and networks.
package infra

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/dopeCape/better-nm/internal/core"
)

// Netlink and sysfs access is wrapped in package-level vars so tests can swap
// in fakes.
var (
	linkList  = netlink.LinkList
	addrList  = netlink.AddrList
	neighList = netlink.NeighList
	sysfsRoot = "/sys/class/net"
)

// HasSysfsDevice reports whether /sys/class/net/<name>/device exists, which is
// true only for links backed by a bus device (PCI, USB, ...): the physical NICs.
func HasSysfsDevice(name string) bool {
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return false
	}
	_, err := os.Stat(sysfsRoot + "/" + name + "/device")
	return err == nil
}

// Links lists every kernel link in the current network namespace. Up means
// administratively up and not operationally down (a bridge with no carrier
// reports false, like `ip link`).
func Links(ctx context.Context) ([]core.Link, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := linkList()
	if err != nil {
		return nil, fmt.Errorf("infra: list links: %w", err)
	}
	names := make(map[int]string, len(raw))
	for _, l := range raw {
		a := l.Attrs()
		names[a.Index] = a.Name
	}
	out := make([]core.Link, 0, len(raw))
	for _, l := range raw {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, toLink(l, names))
	}
	return out, nil
}

func toLink(l netlink.Link, names map[int]string) core.Link {
	a := l.Attrs()
	cl := core.Link{
		Name:      a.Name,
		IfIndex:   a.Index,
		Kind:      linkKind(l),
		Master:    names[a.MasterIndex],
		Up:        a.Flags&net.FlagUp != 0 && a.OperState != netlink.OperDown,
		PeerNetNS: -1,
		MTU:       a.MTU,
	}
	if len(a.HardwareAddr) > 0 {
		cl.HwAddr = a.HardwareAddr.String()
	}
	if cl.Kind == "veth" {
		cl.PeerNetNS = a.NetNsID
	}
	if addrs, err := addrList(l, netlink.FAMILY_ALL); err == nil {
		for _, ad := range addrs {
			if ad.IPNet != nil {
				cl.Addresses = append(cl.Addresses, ad.IPNet.String())
			}
		}
	}
	return cl
}

// linkKind normalises netlink's Type() to the vocabulary core.Link documents:
// "device" for links without IFLA_LINKINFO (physical NICs), "loopback" for lo,
// "tun" for tun/tap, otherwise the kernel's own kind string.
func linkKind(l netlink.Link) string {
	if l.Attrs().Flags&net.FlagLoopback != 0 {
		return "loopback"
	}
	switch t := l.Type(); t {
	case "tuntap":
		return "tun"
	case "":
		return "device"
	default:
		return t
	}
}
