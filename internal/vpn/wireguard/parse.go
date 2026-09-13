// Package wireguard imports wg-quick configuration files into NetworkManager
// WireGuard profiles and presents those profiles as VPNs. ParseConf turns a
// .conf into a core.WireGuardSpec; Adapter drives the profiles through a
// core.NetworkManager (Activate/Deactivate, AddWireGuard) so no root, netlink
// or wg-quick is needed: NM does the privileged work after polkit says yes.
//
// Tested with table-driven parser tests on real-world configs and with
// vpntest.FakeNM for the adapter; nothing here touches D-Bus.
package wireguard

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/dopeCape/better-nm/internal/core"
)

// ErrInvalidConf wraps every parse failure so callers can distinguish a bad
// file from an NM failure.
var ErrInvalidConf = errors.New("wireguard: invalid wg-quick config")

// maxIfaceName is the kernel's IFNAMSIZ-1.
const maxIfaceName = 15

// ParseConf parses a wg-quick INI. name is the profile name (a file name is
// fine; a trailing ".conf" is dropped) and also seeds the interface name.
//
// Mapping: [Interface] PrivateKey/Address/DNS/ListenPort/FwMark/MTU/Table and
// [Peer] PublicKey/PresharedKey/Endpoint/AllowedIPs/PersistentKeepalive land in
// the spec; PreUp/PostUp/PreDown/PostDown/SaveConfig cannot be expressed in NM
// and are listed in Unsupported. Table=off sets AutoDefaultRoute=false;
// Table=auto (or absent) with a 0.0.0.0/0 or ::/0 AllowedIP sets it true.
func ParseConf(name string, r io.Reader) (core.WireGuardSpec, error) {
	spec := core.WireGuardSpec{Name: profileName(name)}
	spec.InterfaceName = ifaceName(spec.Name)

	var (
		section     string // "", "interface", "peer"
		cur         *core.WireGuardPeer
		sawIface    bool
		table       string
		defaultV4   bool
		defaultV6   bool
		unsupported []string
	)
	flushPeer := func() {
		if cur != nil {
			spec.Peers = append(spec.Peers, *cur)
			cur = nil
		}
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return spec, fmt.Errorf("%w: line %d: malformed section header %q", ErrInvalidConf, lineNo, line)
			}
			switch strings.ToLower(strings.TrimSpace(line[1 : len(line)-1])) {
			case "interface":
				flushPeer()
				section = "interface"
				sawIface = true
			case "peer":
				flushPeer()
				section = "peer"
				cur = &core.WireGuardPeer{}
			default:
				return spec, fmt.Errorf("%w: line %d: unknown section %s", ErrInvalidConf, lineNo, line)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return spec, fmt.Errorf("%w: line %d: expected key = value", ErrInvalidConf, lineNo)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		lkey := strings.ToLower(key)
		bad := func(err error) error {
			return fmt.Errorf("%w: line %d: %s: %v", ErrInvalidConf, lineNo, key, err)
		}

		switch section {
		case "interface":
			switch lkey {
			case "privatekey":
				if err := validateKey(value); err != nil {
					return spec, bad(err)
				}
				spec.PrivateKey = value
			case "address":
				for _, a := range splitList(value) {
					p, err := parsePrefix(a)
					if err != nil {
						return spec, bad(err)
					}
					spec.Addresses = append(spec.Addresses, p.String())
				}
			case "dns":
				for _, d := range splitList(value) {
					if ip, err := netip.ParseAddr(d); err == nil {
						spec.DNS = append(spec.DNS, ip.String())
					} else {
						spec.DNSSearch = append(spec.DNSSearch, d)
					}
				}
			case "listenport":
				n, err := parseUint(value, 65535)
				if err != nil {
					return spec, bad(err)
				}
				spec.ListenPort = n
			case "fwmark":
				if strings.EqualFold(value, "off") {
					spec.FwMark = 0
					continue
				}
				n, err := strconv.ParseInt(value, 0, 64)
				if err != nil || n < 0 || n > 0xffffffff {
					return spec, bad(fmt.Errorf("not a 32-bit mark: %q", value))
				}
				spec.FwMark = int(n)
			case "mtu":
				n, err := parseUint(value, 65535)
				if err != nil {
					return spec, bad(err)
				}
				spec.MTU = n
			case "table":
				table = strings.ToLower(value)
			case "preup", "postup", "predown", "postdown", "saveconfig":
				unsupported = append(unsupported, key+" = "+value)
			default:
				return spec, fmt.Errorf("%w: line %d: unknown [Interface] key %q", ErrInvalidConf, lineNo, key)
			}
		case "peer":
			switch lkey {
			case "publickey":
				if err := validateKey(value); err != nil {
					return spec, bad(err)
				}
				cur.PublicKey = value
			case "presharedkey":
				if err := validateKey(value); err != nil {
					return spec, bad(err)
				}
				cur.PresharedKey = value
			case "endpoint":
				if err := validateEndpoint(value); err != nil {
					return spec, bad(err)
				}
				cur.Endpoint = value
			case "allowedips":
				for _, a := range splitList(value) {
					p, err := parsePrefix(a)
					if err != nil {
						return spec, bad(err)
					}
					cur.AllowedIPs = append(cur.AllowedIPs, p.String())
					if p.Bits() == 0 {
						if p.Addr().Is4() {
							defaultV4 = true
						} else {
							defaultV6 = true
						}
					}
				}
			case "persistentkeepalive":
				if strings.EqualFold(value, "off") {
					cur.PersistentKeepalive = 0
					continue
				}
				n, err := parseUint(value, 65535)
				if err != nil {
					return spec, bad(err)
				}
				cur.PersistentKeepalive = n
			default:
				return spec, fmt.Errorf("%w: line %d: unknown [Peer] key %q", ErrInvalidConf, lineNo, key)
			}
		default:
			return spec, fmt.Errorf("%w: line %d: key %q before any section", ErrInvalidConf, lineNo, key)
		}
	}
	if err := sc.Err(); err != nil {
		return spec, fmt.Errorf("%w: read: %v", ErrInvalidConf, err)
	}
	flushPeer()

	if !sawIface {
		return spec, fmt.Errorf("%w: no [Interface] section", ErrInvalidConf)
	}
	if spec.PrivateKey == "" {
		return spec, fmt.Errorf("%w: [Interface] has no PrivateKey", ErrInvalidConf)
	}
	for i, p := range spec.Peers {
		if p.PublicKey == "" {
			return spec, fmt.Errorf("%w: [Peer] %d has no PublicKey", ErrInvalidConf, i+1)
		}
	}

	switch {
	case table == "off":
		spec.AutoDefaultRoute = boolPtr(false)
	case table == "" || table == "auto" || table == "main":
		if defaultV4 || defaultV6 {
			spec.AutoDefaultRoute = boolPtr(true)
		}
	default:
		// A custom routing table: wg-quick would put every route there and
		// never touch the default route. NM has no equivalent.
		spec.AutoDefaultRoute = boolPtr(false)
		unsupported = append(unsupported, "Table = "+table)
	}
	spec.Unsupported = unsupported
	return spec, nil
}

// HasDefaultRoute reports whether any peer routes 0.0.0.0/0 or ::/0.
func HasDefaultRoute(spec core.WireGuardSpec) bool {
	for _, p := range spec.Peers {
		for _, a := range p.AllowedIPs {
			if pfx, err := netip.ParsePrefix(a); err == nil && pfx.Bits() == 0 {
				return true
			}
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }

func profileName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, ".conf")
	if name == "" {
		name = "wg0"
	}
	return name
}

// ifaceName derives a kernel-legal interface name from a profile name:
// [A-Za-z0-9_.-] only, at most 15 bytes, never empty.
func ifaceName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		}
		if b.Len() >= maxIfaceName {
			break
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." {
		return "wg0"
	}
	return s
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parsePrefix accepts "a.b.c.d/n", "a.b.c.d" (=/32), "::1/n" or "::1" (=/128).
func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p, nil
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("not an IP or CIDR: %q", s)
	}
	return netip.PrefixFrom(ip, ip.BitLen()), nil
}

func parseUint(s string, max int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > max {
		return 0, fmt.Errorf("not a number in 0..%d: %q", max, s)
	}
	return n, nil
}

// validateKey checks a Curve25519 key: base64 of exactly 32 bytes.
func validateKey(s string) error {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("not base64: %v", err)
	}
	if len(b) != 32 {
		return fmt.Errorf("key is %d bytes, want 32", len(b))
	}
	return nil
}

// validateEndpoint checks host:port where host is an IP, [IPv6] or a DNS name.
func validateEndpoint(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return fmt.Errorf("endpoint %q: %v", s, err)
	}
	if host == "" {
		return fmt.Errorf("endpoint %q: empty host", s)
	}
	if _, err := parseUint(port, 65535); err != nil || port == "0" {
		return fmt.Errorf("endpoint %q: bad port", s)
	}
	return nil
}
