package nm

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

// settingsDict is NM's a{sa{sv}} connection settings. bnm keeps it raw so
// D-Bus signatures survive a GetSettings -> edit -> Update2 round trip.
type settingsDict = map[string]map[string]dbus.Variant

// props is one interface's a{sv} property map.
type props = map[string]dbus.Variant

// ---- variant readers (tolerant: wrong or missing type yields the zero value) ----

func vStr(m props, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].Value().(string); ok {
		return s
	}
	return ""
}

func vBool(m props, k string) (bool, bool) {
	if m == nil {
		return false, false
	}
	if v, ok := m[k]; ok {
		if b, ok := v.Value().(bool); ok {
			return b, true
		}
	}
	return false, false
}

func vU32(m props, k string) uint32 {
	if m == nil {
		return 0
	}
	switch v := m[k].Value().(type) {
	case uint32:
		return v
	case int32:
		return uint32(v)
	case uint64:
		return uint32(v)
	case int64:
		return uint32(v)
	case uint16:
		return uint32(v)
	case byte:
		return uint32(v)
	}
	return 0
}

func vU64(m props, k string) uint64 {
	if m == nil {
		return 0
	}
	switch v := m[k].Value().(type) {
	case uint64:
		return v
	case int64:
		return uint64(v)
	case uint32:
		return uint64(v)
	case int32:
		return uint64(v)
	}
	return 0
}

func vI64(m props, k string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[k].Value().(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		return int64(v), true
	}
	return 0, false
}

func vU8(m props, k string) uint8 {
	if m == nil {
		return 0
	}
	switch v := m[k].Value().(type) {
	case byte:
		return v
	case uint32:
		return uint8(v)
	}
	return 0
}

func vPath(m props, k string) dbus.ObjectPath {
	if m == nil {
		return ""
	}
	switch v := m[k].Value().(type) {
	case dbus.ObjectPath:
		return v
	case string:
		return dbus.ObjectPath(v)
	}
	return ""
}

func vPaths(m props, k string) []dbus.ObjectPath {
	if m == nil {
		return nil
	}
	switch v := m[k].Value().(type) {
	case []dbus.ObjectPath:
		return v
	case []string:
		out := make([]dbus.ObjectPath, len(v))
		for i := range v {
			out[i] = dbus.ObjectPath(v[i])
		}
		return out
	}
	return nil
}

func vBytes(m props, k string) []byte {
	if m == nil {
		return nil
	}
	switch v := m[k].Value().(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	}
	return nil
}

func vStrs(m props, k string) []string {
	if m == nil {
		return nil
	}
	if v, ok := m[k].Value().([]string); ok {
		return v
	}
	return nil
}

func vDicts(m props, k string) []map[string]dbus.Variant {
	if m == nil {
		return nil
	}
	if v, ok := m[k].Value().([]map[string]dbus.Variant); ok {
		return v
	}
	return nil
}

func vStrMap(m props, k string) map[string]string {
	if m == nil {
		return nil
	}
	if v, ok := m[k].Value().(map[string]string); ok {
		return v
	}
	return nil
}

// realPath says whether an object-path property points at something.
func realPath(p dbus.ObjectPath) bool { return p != "" && p != "/" }

// ---- IP address helpers ----

// ip4FromU32 decodes NM's "network byte order" uint32 (the in_addr bytes as they
// sit in memory, which D-Bus then marshals natively).
func ip4FromU32(u uint32) string {
	var b [4]byte
	binary.NativeEndian.PutUint32(b[:], u)
	return netip.AddrFrom4(b).String()
}

func ip4ToU32(s string) (uint32, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return 0, false
	}
	b := a.As4()
	return binary.NativeEndian.Uint32(b[:]), true
}

func ip6FromBytes(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	return netip.AddrFrom16([16]byte(b)).String()
}

func ip6ToBytes(s string) ([]byte, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is6() {
		return nil, false
	}
	b := a.As16()
	return b[:], true
}

// addressData decodes an aa{sv} of {address, prefix} into CIDRs.
func addressData(entries []map[string]dbus.Variant) []string {
	var out []string
	for _, e := range entries {
		addr := vStr(e, "address")
		if addr == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s/%d", addr, vU32(e, "prefix")))
	}
	return out
}

// makeAddressData encodes CIDRs into aa{sv}; a bare address gets a full-length prefix.
func makeAddressData(cidrs []string, family int) ([]map[string]dbus.Variant, error) {
	out := make([]map[string]dbus.Variant, 0, len(cidrs))
	for _, c := range cidrs {
		addr, prefix, err := splitCIDR(c, family)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]dbus.Variant{
			"address": dbus.MakeVariant(addr),
			"prefix":  dbus.MakeVariant(uint32(prefix)),
		})
	}
	return out, nil
}

func splitCIDR(c string, family int) (string, int, error) {
	c = strings.TrimSpace(c)
	if p, err := netip.ParsePrefix(c); err == nil {
		if family == 4 && !p.Addr().Is4() || family == 6 && !p.Addr().Is6() {
			return "", 0, fmt.Errorf("%q is not an IPv%d address", c, family)
		}
		return p.Addr().String(), p.Bits(), nil
	}
	a, err := netip.ParseAddr(c)
	if err != nil {
		return "", 0, fmt.Errorf("bad address %q: %w", c, err)
	}
	if family == 4 && !a.Is4() || family == 6 && !a.Is6() {
		return "", 0, fmt.Errorf("%q is not an IPv%d address", c, family)
	}
	return a.String(), a.BitLen(), nil
}

// ---- IPConfig <-> ipv4/ipv6 setting dicts ----

// decodeIPConfig reads an ipv4 or ipv6 setting.
func decodeIPConfig(m props, family int) core.IPConfig {
	var c core.IPConfig
	c.Method = core.IPMethod(vStr(m, "method"))
	c.Addresses = addressData(vDicts(m, "address-data"))
	c.Gateway = vStr(m, "gateway")
	switch family {
	case 4:
		if dns, ok := m["dns"].Value().([]uint32); ok {
			for _, u := range dns {
				c.DNS = append(c.DNS, ip4FromU32(u))
			}
		}
	case 6:
		if dns, ok := m["dns"].Value().([][]byte); ok {
			for _, b := range dns {
				if s := ip6FromBytes(b); s != "" {
					c.DNS = append(c.DNS, s)
				}
			}
		}
	}
	c.DNSSearch = vStrs(m, "dns-search")
	c.IgnoreAutoDNS, _ = vBool(m, "ignore-auto-dns")
	c.NeverDefault, _ = vBool(m, "never-default")
	return c
}

// ipKeysOwned are the keys UpdateIPConfig rewrites; every other key of the
// existing dict (route-data, dns-priority, addr-gen-mode, ...) is kept.
var ipKeysOwned = []string{"method", "address-data", "addresses", "gateway", "routes",
	"dns", "dns-search", "ignore-auto-dns", "never-default"}

// encodeIPConfig rebuilds an ipv4/ipv6 dict from cfg on top of existing (may be nil).
// Signatures: address-data aa{sv}, dns au (v4) / aay (v6), dns-search as,
// gateway s, method s, ignore-auto-dns b, never-default b. The deprecated
// addresses/routes arrays are dropped: NM ignores address-data when they are present.
func encodeIPConfig(existing props, cfg core.IPConfig, family int) (props, error) {
	out := props{}
	for k, v := range existing {
		out[k] = v
	}
	for _, k := range ipKeysOwned {
		delete(out, k)
	}
	method := cfg.Method
	if method == "" {
		if len(cfg.Addresses) > 0 {
			method = core.IPManual
		} else {
			method = core.IPAuto
		}
	}
	out["method"] = dbus.MakeVariant(string(method))
	ad, err := makeAddressData(cfg.Addresses, family)
	if err != nil {
		return nil, err
	}
	out["address-data"] = dbus.MakeVariant(ad)
	if cfg.Gateway != "" {
		a, err := netip.ParseAddr(cfg.Gateway)
		if err != nil || (family == 4 && !a.Is4()) || (family == 6 && !a.Is6()) {
			return nil, fmt.Errorf("bad IPv%d gateway %q", family, cfg.Gateway)
		}
		out["gateway"] = dbus.MakeVariant(a.String())
	}
	switch family {
	case 4:
		dns := make([]uint32, 0, len(cfg.DNS))
		for _, s := range cfg.DNS {
			u, ok := ip4ToU32(s)
			if !ok {
				return nil, fmt.Errorf("bad IPv4 DNS server %q", s)
			}
			dns = append(dns, u)
		}
		out["dns"] = dbus.MakeVariant(dns)
	case 6:
		dns := make([][]byte, 0, len(cfg.DNS))
		for _, s := range cfg.DNS {
			b, ok := ip6ToBytes(s)
			if !ok {
				return nil, fmt.Errorf("bad IPv6 DNS server %q", s)
			}
			dns = append(dns, b)
		}
		out["dns"] = dbus.MakeVariant(dns)
	default:
		return nil, fmt.Errorf("bad family %d", family)
	}
	search := cfg.DNSSearch
	if search == nil {
		search = []string{}
	}
	out["dns-search"] = dbus.MakeVariant(search)
	out["ignore-auto-dns"] = dbus.MakeVariant(cfg.IgnoreAutoDNS)
	out["never-default"] = dbus.MakeVariant(cfg.NeverDefault)
	return out, nil
}

// sanitizeForUpdate strips the deprecated ipv4/ipv6 "addresses" and "routes"
// arrays that GetSettings returns: if sent back, NM ignores address-data,
// gateway and route-data. Returns a shallow copy; the input is untouched.
func sanitizeForUpdate(s settingsDict) settingsDict {
	out := make(settingsDict, len(s))
	for name, sec := range s {
		cp := make(props, len(sec))
		for k, v := range sec {
			if (name == settingIPv4 || name == settingIPv6) && (k == "addresses" || k == "routes") {
				continue
			}
			cp[k] = v
		}
		out[name] = cp
	}
	return out
}

// ---- Profile decoding ----

// connMeta is the Settings.Connection object's own properties.
type connMeta struct {
	Filename  string
	VersionID uint64
}

// decodeProfile turns a settings dict into a core.Profile.
func decodeProfile(path dbus.ObjectPath, s settingsDict, meta connMeta, active bool) core.Profile {
	conn := s[settingConnection]
	p := core.Profile{
		UUID:          vStr(conn, "uuid"),
		Name:          vStr(conn, "id"),
		RawType:       vStr(conn, "type"),
		InterfaceName: vStr(conn, "interface-name"),
		Autoconnect:   true,
		Permissions:   vStrs(conn, "permissions"),
		Filename:      meta.Filename,
		VersionID:     meta.VersionID,
		Active:        active,
		NMPath:        string(path),
	}
	p.Type = profileType(p.RawType)
	if ac, ok := vBool(conn, "autoconnect"); ok {
		p.Autoconnect = ac
	}
	if ts := vU64(conn, "timestamp"); ts > 0 {
		p.Timestamp = time.Unix(int64(ts), 0)
	}
	if w := s[settingWifi]; w != nil {
		p.SSID = ssidString(vBytes(w, "ssid"))
		p.Security = profileSecurity(vStr(s[settingWifiSecurity], "key-mgmt"))
	}
	if v := s[settingVPN]; v != nil {
		p.VPNServiceType = vStr(v, "service-type")
		if data := vStrMap(v, "data"); len(data) > 0 {
			p.VPNData = make(map[string]string, len(data))
			for k, val := range data {
				// vpn.data holds only non-secret keys; secrets live in vpn.secrets,
				// which GetSettings never returns. Flag keys are noise for callers.
				if strings.HasSuffix(k, "-flags") {
					continue
				}
				p.VPNData[k] = val
			}
		}
	}
	if wg := s[settingWireGuard]; wg != nil {
		p.WireGuard = decodeWireGuardSetting(wg)
	}
	if ip := s[settingIPv4]; ip != nil {
		p.IPv4 = decodeIPConfig(ip, 4)
	}
	if ip := s[settingIPv6]; ip != nil {
		p.IPv6 = decodeIPConfig(ip, 6)
	}
	return p
}

// decodeWireGuardSetting reads the non-secret part of an NM wireguard setting.
// The private key is never copied out.
func decodeWireGuardSetting(wg props) *core.WireGuardSetting {
	out := &core.WireGuardSetting{
		ListenPort: int(vU32(wg, "listen-port")),
		FwMark:     int(vU32(wg, "fwmark")),
		MTU:        int(vU32(wg, "mtu")),
	}
	for _, peer := range vDicts(wg, "peers") {
		pp := core.WireGuardPeer{
			PublicKey:           vStr(peer, "public-key"),
			Endpoint:            vStr(peer, "endpoint"),
			AllowedIPs:          vStrs(peer, "allowed-ips"),
			PersistentKeepalive: int(vU32(peer, "persistent-keepalive")),
		}
		out.Peers = append(out.Peers, pp)
	}
	return out
}

// ssidString renders an SSID; non-UTF-8 SSIDs are shown as escaped bytes.
func ssidString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	s := string(b)
	if strings.ToValidUTF8(s, "") != s {
		return fmt.Sprintf("%q", b)
	}
	return s
}

// ---- Settings builders ----

// wifiPartialSettings is what AddAndActivateConnection2 gets for a new network;
// NM fills in the rest from the device and access point.
func wifiPartialSettings(ssid string, hidden bool, sec core.WifiSecurity, password, user string) (settingsDict, error) {
	km := keyMgmtFor(sec)
	s := settingsDict{
		settingConnection: {
			"id":   dbus.MakeVariant(ssid),
			"type": dbus.MakeVariant(typeWifi),
		},
		settingWifi: {
			"ssid": dbus.MakeVariant([]byte(ssid)),
			"mode": dbus.MakeVariant("infrastructure"),
		},
	}
	if user != "" {
		s[settingConnection]["permissions"] = dbus.MakeVariant([]string{"user:" + user + ":"})
	}
	if hidden {
		s[settingWifi]["hidden"] = dbus.MakeVariant(true)
	}
	switch sec {
	case core.SecOpen:
		return s, nil
	case core.SecWPAEAP:
		return nil, newErr("connect wifi "+ssid, ErrUnsupported, "WPA-EAP (enterprise) networks are unsupported in v1")
	}
	wsec := props{"key-mgmt": dbus.MakeVariant(km)}
	switch sec {
	case core.SecWPAPSK, core.SecSAE:
		if password == "" {
			return nil, newErr("connect wifi "+ssid, ErrNoSecrets, "this network needs a password")
		}
		wsec["psk"] = dbus.MakeVariant(password)
		wsec["psk-flags"] = dbus.MakeVariant(secretFlagsNone)
	case core.SecWEP:
		if password == "" {
			return nil, newErr("connect wifi "+ssid, ErrNoSecrets, "this network needs a WEP key")
		}
		wsec["wep-key0"] = dbus.MakeVariant(password)
		wsec["wep-key-flags"] = dbus.MakeVariant(secretFlagsNone)
		wsec["wep-key-type"] = dbus.MakeVariant(wepKeyType(password))
		wsec["auth-alg"] = dbus.MakeVariant("open")
	}
	s[settingWifi]["security"] = dbus.MakeVariant(settingWifiSecurity)
	s[settingWifiSecurity] = wsec
	return s, nil
}

// wepKeyType: 1 = hex/ASCII key, 2 = passphrase (NMWepKeyType).
func wepKeyType(key string) uint32 {
	switch len(key) {
	case 5, 13, 10, 26:
		return 1
	}
	return 2
}

// wireguardSettings builds the AddConnection2 dict for a WireGuardSpec.
func wireguardSettings(spec core.WireGuardSpec, uuid, user string) (settingsDict, error) {
	op := "add wireguard " + spec.Name
	if spec.Name == "" {
		return nil, newErr(op, nil, "profile name is required")
	}
	if spec.InterfaceName == "" {
		return nil, newErr(op, nil, "interface name is required")
	}
	if spec.PrivateKey == "" {
		return nil, newErr(op, nil, "private key is required")
	}
	conn := props{
		"type":           dbus.MakeVariant(typeWireGuard),
		"id":             dbus.MakeVariant(spec.Name),
		"uuid":           dbus.MakeVariant(uuid),
		"interface-name": dbus.MakeVariant(spec.InterfaceName),
		"autoconnect":    dbus.MakeVariant(spec.Autoconnect),
	}
	if user != "" {
		conn["permissions"] = dbus.MakeVariant([]string{"user:" + user + ":"})
	}
	wg := props{
		"private-key":       dbus.MakeVariant(spec.PrivateKey),
		"private-key-flags": dbus.MakeVariant(secretFlagsNone),
	}
	if spec.ListenPort > 0 {
		wg["listen-port"] = dbus.MakeVariant(uint32(spec.ListenPort))
	}
	if spec.FwMark > 0 {
		wg["fwmark"] = dbus.MakeVariant(uint32(spec.FwMark))
	}
	if spec.MTU > 0 {
		wg["mtu"] = dbus.MakeVariant(uint32(spec.MTU))
	}
	if spec.AutoDefaultRoute != nil {
		// NMTernary: -1 default, 0 false, 1 true.
		var t int32
		if *spec.AutoDefaultRoute {
			t = 1
		}
		wg["ip4-auto-default-route"] = dbus.MakeVariant(t)
		wg["ip6-auto-default-route"] = dbus.MakeVariant(t)
	}
	peers := make([]map[string]dbus.Variant, 0, len(spec.Peers))
	for i, p := range spec.Peers {
		if p.PublicKey == "" {
			return nil, newErr(op, nil, fmt.Sprintf("peer %d has no public key", i))
		}
		peer := map[string]dbus.Variant{
			"public-key": dbus.MakeVariant(p.PublicKey),
		}
		allowed := p.AllowedIPs
		if allowed == nil {
			allowed = []string{}
		}
		peer["allowed-ips"] = dbus.MakeVariant(allowed)
		if p.Endpoint != "" {
			peer["endpoint"] = dbus.MakeVariant(p.Endpoint)
		}
		if p.PersistentKeepalive > 0 {
			peer["persistent-keepalive"] = dbus.MakeVariant(uint32(p.PersistentKeepalive))
		}
		if p.PresharedKey != "" {
			peer["preshared-key"] = dbus.MakeVariant(p.PresharedKey)
			peer["preshared-key-flags"] = dbus.MakeVariant(secretFlagsNone)
		}
		peers = append(peers, peer)
	}
	wg["peers"] = dbus.MakeVariant(peers)

	var a4, a6, d4, d6 []string
	for _, a := range spec.Addresses {
		addr, _, err := splitCIDR(a, 0)
		if err != nil {
			return nil, newErr(op, nil, err.Error())
		}
		if ip, _ := netip.ParseAddr(addr); ip.Is4() {
			a4 = append(a4, a)
		} else {
			a6 = append(a6, a)
		}
	}
	for _, d := range spec.DNS {
		ip, err := netip.ParseAddr(d)
		if err != nil {
			return nil, newErr(op, nil, fmt.Sprintf("bad DNS server %q", d))
		}
		if ip.Is4() {
			d4 = append(d4, d)
		} else {
			d6 = append(d6, d)
		}
	}
	ipCfg := func(addrs, dns []string, family int) (props, error) {
		if len(addrs) == 0 {
			return encodeIPConfig(nil, core.IPConfig{Method: core.IPDisabled}, family)
		}
		cfg := core.IPConfig{Method: core.IPManual, Addresses: addrs, DNS: dns, DNSSearch: spec.DNSSearch}
		return encodeIPConfig(nil, cfg, family)
	}
	ip4, err := ipCfg(a4, d4, 4)
	if err != nil {
		return nil, newErr(op, nil, err.Error())
	}
	ip6, err := ipCfg(a6, d6, 6)
	if err != nil {
		return nil, newErr(op, nil, err.Error())
	}
	return settingsDict{
		settingConnection: conn,
		settingWireGuard:  wg,
		settingIPv4:       ip4,
		settingIPv6:       ip6,
	}, nil
}
