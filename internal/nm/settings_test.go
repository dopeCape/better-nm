package nm

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

func sig(t *testing.T, m props, key, want string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("key %q missing", key)
	}
	if got := v.Signature().String(); got != want {
		t.Fatalf("%s: signature %q want %q", key, got, want)
	}
}

func TestIP4Codec(t *testing.T) {
	// 192.168.1.254 as NM's Nameservers "au" value on the dev machine.
	const live uint32 = 4261521600
	if got := ip4FromU32(live); got != "192.168.1.254" {
		t.Fatalf("ip4FromU32=%q", got)
	}
	u, ok := ip4ToU32("192.168.1.254")
	if !ok || u != live {
		t.Fatalf("ip4ToU32=%d,%v want %d", u, ok, live)
	}
	if _, ok := ip4ToU32("::1"); ok {
		t.Fatal("v6 must not encode as v4")
	}
	b, ok := ip6ToBytes("fd7a:115c:a1e0::aa2d:3433")
	if !ok || len(b) != 16 || ip6FromBytes(b) != "fd7a:115c:a1e0::aa2d:3433" {
		t.Fatalf("v6 round trip failed: %v %v", b, ok)
	}
	if ip6FromBytes([]byte{1, 2}) != "" {
		t.Fatal("short v6 must be empty")
	}
}

func TestEncodeIPConfigV4(t *testing.T) {
	existing := props{
		"method":       dbus.MakeVariant("auto"),
		"addresses":    dbus.MakeVariant([][]uint32{{1, 2, 3}}),
		"routes":       dbus.MakeVariant([][]uint32{}),
		"dns-priority": dbus.MakeVariant(int32(-5)),
		"route-data":   dbus.MakeVariant([]map[string]dbus.Variant{}),
	}
	cfg := core.IPConfig{
		Method:        core.IPManual,
		Addresses:     []string{"10.0.0.5/24", "10.0.0.6"},
		Gateway:       "10.0.0.1",
		DNS:           []string{"1.1.1.1", "8.8.8.8"},
		DNSSearch:     []string{"corp.example", "~lab"},
		IgnoreAutoDNS: true,
		NeverDefault:  true,
	}
	out, err := encodeIPConfig(existing, cfg, 4)
	if err != nil {
		t.Fatal(err)
	}
	sig(t, out, "method", "s")
	sig(t, out, "address-data", "aa{sv}")
	sig(t, out, "gateway", "s")
	sig(t, out, "dns", "au")
	sig(t, out, "dns-search", "as")
	sig(t, out, "ignore-auto-dns", "b")
	sig(t, out, "never-default", "b")
	sig(t, out, "dns-priority", "i")
	sig(t, out, "route-data", "aa{sv}")
	for _, gone := range []string{"addresses", "routes"} {
		if _, ok := out[gone]; ok {
			t.Errorf("deprecated %q must be stripped", gone)
		}
	}
	ad := out["address-data"].Value().([]map[string]dbus.Variant)
	if len(ad) != 2 || ad[0]["address"].Value() != "10.0.0.5" || ad[0]["prefix"].Value() != uint32(24) ||
		ad[1]["address"].Value() != "10.0.0.6" || ad[1]["prefix"].Value() != uint32(32) {
		t.Fatalf("address-data=%v", ad)
	}
	if ad[0]["prefix"].Signature().String() != "u" {
		t.Fatal("prefix must be u")
	}
	dns := out["dns"].Value().([]uint32)
	if len(dns) != 2 || ip4FromU32(dns[0]) != "1.1.1.1" || ip4FromU32(dns[1]) != "8.8.8.8" {
		t.Fatalf("dns=%v", dns)
	}
	// Whole-dict signature as NM receives it.
	whole := settingsDict{"ipv4": out}
	if s := dbus.SignatureOf(whole).String(); s != "a{sa{sv}}" {
		t.Fatalf("settings signature %q", s)
	}
	// Decode back (a bare address comes back with its full prefix).
	back := decodeIPConfig(out, 4)
	cfg.Addresses[1] = "10.0.0.6/32"
	if !reflect.DeepEqual(back, cfg) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", back, cfg)
	}
	if existing["addresses"].Signature().String() != "aau" {
		t.Fatal("input must be untouched")
	}
}

func TestEncodeIPConfigV6(t *testing.T) {
	cfg := core.IPConfig{
		Method:    core.IPManual,
		Addresses: []string{"2001:db8::10/64"},
		Gateway:   "2001:db8::1",
		DNS:       []string{"2606:4700:4700::1111"},
	}
	out, err := encodeIPConfig(nil, cfg, 6)
	if err != nil {
		t.Fatal(err)
	}
	sig(t, out, "dns", "aay")
	sig(t, out, "address-data", "aa{sv}")
	sig(t, out, "dns-search", "as")
	dns := out["dns"].Value().([][]byte)
	if len(dns) != 1 || ip6FromBytes(dns[0]) != "2606:4700:4700::1111" {
		t.Fatalf("dns=%v", dns)
	}
	back := decodeIPConfig(out, 6)
	if back.Method != core.IPManual || back.Gateway != "2001:db8::1" || len(back.Addresses) != 1 || back.Addresses[0] != "2001:db8::10/64" || len(back.DNS) != 1 {
		t.Fatalf("round trip %+v", back)
	}
}

func TestEncodeIPConfigDefaultsAndErrors(t *testing.T) {
	out, err := encodeIPConfig(nil, core.IPConfig{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if out["method"].Value() != "auto" {
		t.Fatalf("empty config must be auto, got %v", out["method"].Value())
	}
	if _, ok := out["gateway"]; ok {
		t.Fatal("no gateway key when empty")
	}
	if len(out["dns"].Value().([]uint32)) != 0 || len(out["dns-search"].Value().([]string)) != 0 {
		t.Fatal("dns/dns-search must be empty arrays, not missing")
	}
	out, err = encodeIPConfig(nil, core.IPConfig{Addresses: []string{"10.1.1.1/24"}}, 4)
	if err != nil || out["method"].Value() != "manual" {
		t.Fatalf("addresses without method must imply manual: %v %v", out["method"].Value(), err)
	}
	bad := []struct {
		name   string
		cfg    core.IPConfig
		family int
	}{
		{"v6 addr in v4", core.IPConfig{Addresses: []string{"::1/128"}}, 4},
		{"garbage addr", core.IPConfig{Addresses: []string{"nope"}}, 4},
		{"bad gateway", core.IPConfig{Gateway: "10.0.0"}, 4},
		{"v4 gateway in v6", core.IPConfig{Gateway: "10.0.0.1"}, 6},
		{"bad dns", core.IPConfig{DNS: []string{"one.one.one.one"}}, 4},
		{"v6 dns in v4", core.IPConfig{DNS: []string{"::1"}}, 4},
		{"bad family", core.IPConfig{}, 5},
	}
	for _, tt := range bad {
		if _, err := encodeIPConfig(nil, tt.cfg, tt.family); err == nil {
			t.Errorf("%s: expected error", tt.name)
		}
	}
}

func TestSanitizeForUpdate(t *testing.T) {
	in := settingsDict{
		"ipv4":            {"addresses": dbus.MakeVariant([][]uint32{}), "routes": dbus.MakeVariant([][]uint32{}), "method": dbus.MakeVariant("auto")},
		"ipv6":            {"addresses": dbus.MakeVariant([]any{}), "routes": dbus.MakeVariant([]any{}), "address-data": dbus.MakeVariant([]map[string]dbus.Variant{})},
		"802-11-wireless": {"addresses": dbus.MakeVariant("keep me")},
	}
	out := sanitizeForUpdate(in)
	if _, ok := out["ipv4"]["addresses"]; ok {
		t.Error("ipv4.addresses kept")
	}
	if _, ok := out["ipv6"]["routes"]; ok {
		t.Error("ipv6.routes kept")
	}
	if _, ok := out["ipv6"]["address-data"]; !ok {
		t.Error("address-data dropped")
	}
	if _, ok := out["802-11-wireless"]["addresses"]; !ok {
		t.Error("unrelated section touched")
	}
	if _, ok := in["ipv4"]["addresses"]; !ok {
		t.Error("input mutated")
	}
	out["ipv4"]["x"] = dbus.MakeVariant(1)
	if _, ok := in["ipv4"]["x"]; ok {
		t.Error("output aliases input")
	}
}

func TestDecodeProfile(t *testing.T) {
	s := settingsDict{
		"connection": {
			"id": dbus.MakeVariant("ALHN-F832-5"), "uuid": dbus.MakeVariant("8694ab34-981a-41dc-a11b-85f4196a891a"),
			"type": dbus.MakeVariant("802-11-wireless"), "interface-name": dbus.MakeVariant("wlp4s0"),
			"permissions": dbus.MakeVariant([]string{"user:baby:"}), "timestamp": dbus.MakeVariant(uint64(1789320813)),
			"autoconnect": dbus.MakeVariant(false),
		},
		"802-11-wireless":          {"ssid": dbus.MakeVariant([]byte("ALHN-F832-5")), "mode": dbus.MakeVariant("infrastructure")},
		"802-11-wireless-security": {"key-mgmt": dbus.MakeVariant("sae")},
		"ipv4": {"method": dbus.MakeVariant("manual"), "address-data": dbus.MakeVariant([]map[string]dbus.Variant{
			{"address": dbus.MakeVariant("192.168.1.73"), "prefix": dbus.MakeVariant(uint32(24))}}),
			"gateway": dbus.MakeVariant("192.168.1.254"), "dns": dbus.MakeVariant([]uint32{4261521600})},
		"ipv6": {"method": dbus.MakeVariant("auto"), "dns": dbus.MakeVariant([][]byte{mustV6("2606:4700:4700::1111")})},
	}
	p := decodeProfile("/org/freedesktop/NetworkManager/Settings/2", s, connMeta{Filename: "/etc/x.nmconnection", VersionID: 7}, true)
	want := core.Profile{
		UUID: "8694ab34-981a-41dc-a11b-85f4196a891a", Name: "ALHN-F832-5", Type: core.ProfileWifi, RawType: "802-11-wireless",
		InterfaceName: "wlp4s0", Autoconnect: false, SSID: "ALHN-F832-5", Security: core.SecSAE,
		Timestamp:   time.Unix(1789320813, 0),
		IPv4:        core.IPConfig{Method: core.IPManual, Addresses: []string{"192.168.1.73/24"}, Gateway: "192.168.1.254", DNS: []string{"192.168.1.254"}},
		IPv6:        core.IPConfig{Method: core.IPAuto, DNS: []string{"2606:4700:4700::1111"}},
		Permissions: []string{"user:baby:"}, Filename: "/etc/x.nmconnection", VersionID: 7, Active: true,
		NMPath: "/org/freedesktop/NetworkManager/Settings/2",
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("decodeProfile:\n got %+v\nwant %+v", p, want)
	}

	vpn := decodeProfile("/x", settingsDict{
		"connection": {"id": dbus.MakeVariant("tejas"), "type": dbus.MakeVariant("vpn"), "uuid": dbus.MakeVariant("u")},
		"vpn":        {"service-type": dbus.MakeVariant("org.freedesktop.NetworkManager.openvpn"), "data": dbus.MakeVariant(map[string]string{"remote": "vpn.example"})},
	}, connMeta{}, false)
	if vpn.Type != core.ProfileVPN || vpn.VPNServiceType != "org.freedesktop.NetworkManager.openvpn" || !vpn.Autoconnect || vpn.Security != "" {
		t.Fatalf("vpn profile %+v", vpn)
	}
	open := decodeProfile("/y", settingsDict{
		"connection":      {"type": dbus.MakeVariant("802-11-wireless")},
		"802-11-wireless": {"ssid": dbus.MakeVariant([]byte{0xff, 0xfe, 'x'})},
	}, connMeta{}, false)
	if open.Security != core.SecOpen || open.SSID != `"\xff\xfex"` {
		t.Fatalf("open/non-utf8 profile %+v", open)
	}
}

func mustV6(s string) []byte {
	b, _ := ip6ToBytes(s)
	return b
}

func TestWifiPartialSettings(t *testing.T) {
	s, err := wifiPartialSettings("Cafe", false, core.SecWPAPSK, "hunter22", "baby")
	if err != nil {
		t.Fatal(err)
	}
	if dbus.SignatureOf(s).String() != "a{sa{sv}}" {
		t.Fatal("bad settings signature")
	}
	sig(t, s["connection"], "id", "s")
	sig(t, s["connection"], "type", "s")
	sig(t, s["connection"], "permissions", "as")
	if perms := s["connection"]["permissions"].Value().([]string); len(perms) != 1 || perms[0] != "user:baby:" {
		t.Fatalf("permissions=%v", perms)
	}
	sig(t, s["802-11-wireless"], "ssid", "ay")
	if _, ok := s["802-11-wireless"]["hidden"]; ok {
		t.Fatal("hidden must be absent when false")
	}
	sec := s["802-11-wireless-security"]
	sig(t, sec, "key-mgmt", "s")
	sig(t, sec, "psk", "s")
	sig(t, sec, "psk-flags", "u")
	if sec["key-mgmt"].Value() != "wpa-psk" || sec["psk"].Value() != "hunter22" || sec["psk-flags"].Value() != uint32(0) {
		t.Fatalf("security=%v", sec)
	}

	s, err = wifiPartialSettings("Hidden", true, core.SecSAE, "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	if s["802-11-wireless"]["hidden"].Value() != true || s["802-11-wireless-security"]["key-mgmt"].Value() != "sae" {
		t.Fatalf("sae/hidden settings %v", s)
	}
	if _, ok := s["connection"]["permissions"]; ok {
		t.Fatal("no permissions without a user name")
	}

	s, err = wifiPartialSettings("Open", false, core.SecOpen, "", "baby")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s["802-11-wireless-security"]; ok {
		t.Fatal("open network must have no security section")
	}

	s, err = wifiPartialSettings("Owe", false, core.SecOWE, "", "baby")
	if err != nil || s["802-11-wireless-security"]["key-mgmt"].Value() != "owe" {
		t.Fatalf("owe: %v %v", s, err)
	}

	s, err = wifiPartialSettings("Wep", false, core.SecWEP, "abcde", "baby")
	if err != nil || s["802-11-wireless-security"]["key-mgmt"].Value() != "none" || s["802-11-wireless-security"]["wep-key-type"].Value() != uint32(1) {
		t.Fatalf("wep: %v %v", s, err)
	}

	if _, err := wifiPartialSettings("Corp", false, core.SecWPAEAP, "", "baby"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("eap must be ErrUnsupported, got %v", err)
	}
	if _, err := wifiPartialSettings("Cafe", false, core.SecWPAPSK, "", "baby"); !errors.Is(err, ErrNoSecrets) {
		t.Fatalf("psk without password must be ErrNoSecrets, got %v", err)
	}
}

func TestWireguardSettings(t *testing.T) {
	yes := true
	spec := core.WireGuardSpec{
		Name: "corp", InterfaceName: "wg-corp", PrivateKey: "PRIV=", ListenPort: 51820, FwMark: 0xca6c, MTU: 1420,
		Addresses: []string{"10.8.0.2/24", "fd00::2/64"}, DNS: []string{"10.8.0.1", "fd00::1"}, DNSSearch: []string{"corp"},
		Peers: []core.WireGuardPeer{{
			PublicKey: "PUB=", PresharedKey: "PSK=", Endpoint: "vpn.example:51820",
			AllowedIPs: []string{"0.0.0.0/0", "::/0"}, PersistentKeepalive: 25,
		}},
		AutoDefaultRoute: &yes, Autoconnect: true,
	}
	s, err := wireguardSettings(spec, "11111111-2222-4333-8444-555555555555", "baby")
	if err != nil {
		t.Fatal(err)
	}
	if dbus.SignatureOf(s).String() != "a{sa{sv}}" {
		t.Fatal("bad settings signature")
	}
	conn := s["connection"]
	if conn["type"].Value() != "wireguard" || conn["id"].Value() != "corp" || conn["interface-name"].Value() != "wg-corp" ||
		conn["uuid"].Value() != "11111111-2222-4333-8444-555555555555" || conn["autoconnect"].Value() != true {
		t.Fatalf("connection=%v", conn)
	}
	sig(t, conn, "permissions", "as")
	wg := s["wireguard"]
	sig(t, wg, "private-key", "s")
	sig(t, wg, "private-key-flags", "u")
	sig(t, wg, "listen-port", "u")
	sig(t, wg, "fwmark", "u")
	sig(t, wg, "mtu", "u")
	sig(t, wg, "ip4-auto-default-route", "i")
	sig(t, wg, "ip6-auto-default-route", "i")
	sig(t, wg, "peers", "aa{sv}")
	if wg["ip4-auto-default-route"].Value() != int32(1) {
		t.Fatal("auto default route true must be NMTernary 1")
	}
	peers := wg["peers"].Value().([]map[string]dbus.Variant)
	if len(peers) != 1 {
		t.Fatal("one peer expected")
	}
	p := peers[0]
	sig(t, p, "public-key", "s")
	sig(t, p, "preshared-key", "s")
	sig(t, p, "preshared-key-flags", "u")
	sig(t, p, "endpoint", "s")
	sig(t, p, "allowed-ips", "as")
	sig(t, p, "persistent-keepalive", "u")
	if p["persistent-keepalive"].Value() != uint32(25) {
		t.Fatal("keepalive")
	}
	ip4, ip6 := s["ipv4"], s["ipv6"]
	if ip4["method"].Value() != "manual" || ip6["method"].Value() != "manual" {
		t.Fatalf("methods %v %v", ip4["method"].Value(), ip6["method"].Value())
	}
	if _, ok := ip4["gateway"]; ok {
		t.Fatal("wireguard must never set a gateway")
	}
	sig(t, ip4, "dns", "au")
	sig(t, ip6, "dns", "aay")
	if ip4FromU32(ip4["dns"].Value().([]uint32)[0]) != "10.8.0.1" || ip6FromBytes(ip6["dns"].Value().([][]byte)[0]) != "fd00::1" {
		t.Fatal("dns split by family wrong")
	}
	if ad := addressData(ip4["address-data"].Value().([]map[string]dbus.Variant)); len(ad) != 1 || ad[0] != "10.8.0.2/24" {
		t.Fatalf("v4 address-data %v", ad)
	}
	if ad := addressData(ip6["address-data"].Value().([]map[string]dbus.Variant)); len(ad) != 1 || ad[0] != "fd00::2/64" {
		t.Fatalf("v6 address-data %v", ad)
	}
	if ds := ip4["dns-search"].Value().([]string); len(ds) != 1 || ds[0] != "corp" {
		t.Fatalf("dns-search %v", ds)
	}

	// v4 only, nil AutoDefaultRoute, minimal peer.
	s, err = wireguardSettings(core.WireGuardSpec{
		Name: "min", InterfaceName: "wg0", PrivateKey: "k", Addresses: []string{"10.0.0.2/32"},
		Peers: []core.WireGuardPeer{{PublicKey: "p"}},
	}, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s["wireguard"]["ip4-auto-default-route"]; ok {
		t.Fatal("nil AutoDefaultRoute must leave NM's default")
	}
	for _, k := range []string{"listen-port", "fwmark", "mtu"} {
		if _, ok := s["wireguard"][k]; ok {
			t.Errorf("%s must be absent when zero", k)
		}
	}
	if s["ipv6"]["method"].Value() != "disabled" {
		t.Fatal("no v6 address means ipv6 disabled")
	}
	if _, ok := s["connection"]["permissions"]; ok {
		t.Fatal("no permissions without a user")
	}
	peer := s["wireguard"]["peers"].Value().([]map[string]dbus.Variant)[0]
	if _, ok := peer["preshared-key"]; ok {
		t.Fatal("no psk key when empty")
	}
	if peer["allowed-ips"].Signature().String() != "as" {
		t.Fatal("allowed-ips must be as even when empty")
	}

	bad := []core.WireGuardSpec{
		{InterfaceName: "wg0", PrivateKey: "k"},
		{Name: "n", PrivateKey: "k"},
		{Name: "n", InterfaceName: "wg0"},
		{Name: "n", InterfaceName: "wg0", PrivateKey: "k", Peers: []core.WireGuardPeer{{}}},
		{Name: "n", InterfaceName: "wg0", PrivateKey: "k", Addresses: []string{"nope"}},
		{Name: "n", InterfaceName: "wg0", PrivateKey: "k", DNS: []string{"nope"}},
	}
	for i, b := range bad {
		if _, err := wireguardSettings(b, "u", ""); err == nil {
			t.Errorf("bad spec %d accepted", i)
		}
	}
}

func TestVariantReaders(t *testing.T) {
	m := props{
		"s": dbus.MakeVariant("x"), "u": dbus.MakeVariant(uint32(7)), "i": dbus.MakeVariant(int32(-1)),
		"t": dbus.MakeVariant(uint64(9)), "x": dbus.MakeVariant(int64(-3)), "y": dbus.MakeVariant(byte(200)),
		"b": dbus.MakeVariant(true), "o": dbus.MakeVariant(dbus.ObjectPath("/a")),
		"ao": dbus.MakeVariant([]dbus.ObjectPath{"/a", "/b"}), "ay": dbus.MakeVariant([]byte("hi")),
		"as": dbus.MakeVariant([]string{"q"}), "a{ss}": dbus.MakeVariant(map[string]string{"k": "v"}),
	}
	if vStr(m, "s") != "x" || vStr(m, "u") != "" || vStr(nil, "s") != "" {
		t.Error("vStr")
	}
	if vU32(m, "u") != 7 || vU32(m, "i") != 0xffffffff || vU32(m, "y") != 200 || vU32(m, "missing") != 0 {
		t.Error("vU32")
	}
	if vU64(m, "t") != 9 || vU64(m, "u") != 7 {
		t.Error("vU64")
	}
	if v, ok := vI64(m, "x"); !ok || v != -3 {
		t.Error("vI64")
	}
	if _, ok := vI64(m, "s"); ok {
		t.Error("vI64 on string")
	}
	if vU8(m, "y") != 200 || vU8(m, "u") != 7 {
		t.Error("vU8")
	}
	if b, ok := vBool(m, "b"); !ok || !b {
		t.Error("vBool")
	}
	if _, ok := vBool(m, "s"); ok {
		t.Error("vBool on string")
	}
	if vPath(m, "o") != "/a" || len(vPaths(m, "ao")) != 2 || string(vBytes(m, "ay")) != "hi" || vStrs(m, "as")[0] != "q" || vStrMap(m, "a{ss}")["k"] != "v" {
		t.Error("compound readers")
	}
	if realPath("/") || realPath("") || !realPath("/a") {
		t.Error("realPath")
	}
}

func TestDecodeProfileVPNDataAndWireGuard(t *testing.T) {
	s := settingsDict{
		settingConnection: {
			"uuid": dbus.MakeVariant("u1"), "id": dbus.MakeVariant("work"), "type": dbus.MakeVariant("vpn"),
		},
		settingVPN: {
			"service-type": dbus.MakeVariant("org.freedesktop.NetworkManager.openvpn"),
			"data": dbus.MakeVariant(map[string]string{
				"remote": "vpn.example.com:1194:udp", "username": "me", "password-flags": "1",
			}),
		},
	}
	p := decodeProfile("/p", s, connMeta{}, false)
	if p.VPNData["remote"] != "vpn.example.com:1194:udp" || p.VPNData["username"] != "me" {
		t.Fatalf("vpn data not decoded: %#v", p.VPNData)
	}
	if _, ok := p.VPNData["password-flags"]; ok {
		t.Fatalf("flag keys should be dropped: %#v", p.VPNData)
	}

	wg := settingsDict{
		settingConnection: {"uuid": dbus.MakeVariant("u2"), "id": dbus.MakeVariant("wg0"), "type": dbus.MakeVariant("wireguard")},
		settingWireGuard: {
			"private-key": dbus.MakeVariant("SECRET"),
			"listen-port": dbus.MakeVariant(uint32(51820)),
			"mtu":         dbus.MakeVariant(uint32(1420)),
			"peers": dbus.MakeVariant([]map[string]dbus.Variant{{
				"public-key":           dbus.MakeVariant("PUB"),
				"endpoint":             dbus.MakeVariant("1.2.3.4:51820"),
				"allowed-ips":          dbus.MakeVariant([]string{"0.0.0.0/0"}),
				"persistent-keepalive": dbus.MakeVariant(uint32(25)),
			}}),
		},
	}
	q := decodeProfile("/q", wg, connMeta{}, false)
	if q.WireGuard == nil || q.WireGuard.ListenPort != 51820 || q.WireGuard.MTU != 1420 {
		t.Fatalf("wireguard setting not decoded: %#v", q.WireGuard)
	}
	if len(q.WireGuard.Peers) != 1 || q.WireGuard.Peers[0].PublicKey != "PUB" || q.WireGuard.Peers[0].PersistentKeepalive != 25 {
		t.Fatalf("peer not decoded: %#v", q.WireGuard.Peers)
	}
	if strings.Contains(fmt.Sprint(q), "SECRET") {
		t.Fatal("private key leaked into Profile")
	}
}
