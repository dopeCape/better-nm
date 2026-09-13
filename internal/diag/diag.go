// Package diag owns the read-only diagnostics behind `bnm diag ...` and the
// TUI's Advanced tab: hosts on the LAN (neighbour table, optionally refreshed
// by an unprivileged ping-socket sweep, named via mDNS and reverse DNS),
// listening sockets on this host with process attribution where /proc allows
// it, the kernel route tables, DNS lookups with timing, and the public IP as
// seen by Cloudflare.
//
// Nothing here needs root: rtnetlink dumps, /proc/net, ICMP datagram sockets
// (net.ipv4.ping_group_range) and plain UDP/HTTP. When a capability is
// missing the function still returns what it could read and the error carries
// the one-time fix.
//
// Tested with fixture files under testdata for the /proc parsers, fakes for
// the netlink calls (package-level func vars), an httptest server for the
// public-IP parser, DNS tests that skip offline, and a `//go:build live` test
// that runs every function on this machine.
package diag

import (
	"errors"
	"fmt"
	"time"

	"github.com/vishvananda/netlink"
)

// Netlink access is wrapped in package-level vars so tests can swap in fakes.
var (
	linkByName        = netlink.LinkByName
	linkList          = netlink.LinkList
	addrList          = netlink.AddrList
	neighList         = netlink.NeighList
	routeListFiltered = netlink.RouteListFiltered
	procRoot          = "/proc"
)

// ErrNeedsPingGroup is returned (wrapped) when the ICMP datagram socket is
// refused; the LAN table is still returned alongside it.
var ErrNeedsPingGroup = errors.New("diag: ping socket refused (EACCES); one-time fix: add `net.ipv4.ping_group_range = 0 2147483647` to /etc/sysctl.d/50-bnm.conf and run `sysctl --system`")

// clockTick is the kernel's USER_HZ used for neighbour timestamps.
const clockTick = 100

func ticksAgo(now time.Time, ticks uint32) time.Time {
	return now.Add(-time.Duration(ticks) * time.Second / clockTick)
}

// SweepError is the non-fatal error LANHosts returns when the sweep could not
// run but the neighbour table was read; the hosts are still valid.
type SweepError struct{ Err error }

func (e *SweepError) Error() string { return fmt.Sprintf("diag: sweep skipped: %v", e.Err) }
func (e *SweepError) Unwrap() error { return e.Err }
