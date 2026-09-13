package wireguard

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dopeCape/better-nm/internal/core"
)

// Keys below are random 32-byte values, not real credentials.
const (
	keyA = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	keyB = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
	keyC = "TrMvSoP4jYQlY6RIzBgbssQqY3vxI2Pi+y71lOWWXX0="
	keyD = "gN65BkIKy1eCE9pP1wdc8ROUtkHLF2PfAqYdyYBz6EA="
)

const mullvadConf = `[Interface]
# Device: Brave Otter
PrivateKey = ` + keyA + `
Address = 10.64.222.21/32,fc00:bbbb:bbbb:bb01::1:de14/128
DNS = 10.64.0.1

[Peer]
PublicKey = ` + keyB + `
AllowedIPs = 0.0.0.0/0,::0/0
Endpoint = 185.65.134.66:51820
`

const siteToSiteConf = `[Interface]
Address = 192.168.100.1/24
ListenPort = 51820
PrivateKey = ` + keyA + `
Table = off
FwMark = 0x8888
MTU = 1420
PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE
SaveConfig = true

[Peer]
# office
PublicKey = ` + keyB + `
PresharedKey = ` + keyC + `
AllowedIPs = 192.168.100.2/32, 10.10.0.0/16
Endpoint = office.example.com:51820
PersistentKeepalive = 25

[Peer]
# home
PublicKey = ` + keyD + `
AllowedIPs = 192.168.100.3/32
PersistentKeepalive = off
`

const ipv6Conf = `[Interface]
PrivateKey = ` + keyA + `
Address = fd86:ea04:1111::2/64, 10.200.200.2
DNS = fd86:ea04:1111::1, 1.1.1.1, corp.example, lab.example
Table = auto

[Peer]
PublicKey = ` + keyB + `
Endpoint = [2001:db8::1]:51820
AllowedIPs = ::/0
AllowedIPs = 10.200.200.0/24
`

func TestParseConf(t *testing.T) {
	tr, fa := true, false
	cases := []struct {
		name string
		file string
		in   string
		want core.WireGuardSpec
	}{
		{
			name: "mullvad", file: "mullvad-se-sto-wg-001.conf", in: mullvadConf,
			want: core.WireGuardSpec{
				Name: "mullvad-se-sto-wg-001", InterfaceName: "mullvad-se-sto-",
				PrivateKey: keyA,
				Addresses:  []string{"10.64.222.21/32", "fc00:bbbb:bbbb:bb01::1:de14/128"},
				DNS:        []string{"10.64.0.1"},
				Peers: []core.WireGuardPeer{{
					PublicKey: keyB, AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Endpoint: "185.65.134.66:51820",
				}},
				AutoDefaultRoute: &tr,
			},
		},
		{
			name: "site-to-site", file: "/etc/wireguard/wg0.conf", in: siteToSiteConf,
			want: core.WireGuardSpec{
				Name: "wg0", InterfaceName: "wg0",
				PrivateKey: keyA, ListenPort: 51820, FwMark: 0x8888, MTU: 1420,
				Addresses: []string{"192.168.100.1/24"},
				Peers: []core.WireGuardPeer{
					{PublicKey: keyB, PresharedKey: keyC, AllowedIPs: []string{"192.168.100.2/32", "10.10.0.0/16"},
						Endpoint: "office.example.com:51820", PersistentKeepalive: 25},
					{PublicKey: keyD, AllowedIPs: []string{"192.168.100.3/32"}},
				},
				AutoDefaultRoute: &fa,
				Unsupported: []string{
					"PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE",
					"PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE",
					"SaveConfig = true",
				},
			},
		},
		{
			name: "ipv6", file: "v6 tunnel.conf", in: ipv6Conf,
			want: core.WireGuardSpec{
				Name: "v6 tunnel", InterfaceName: "v6tunnel",
				PrivateKey: keyA,
				Addresses:  []string{"fd86:ea04:1111::2/64", "10.200.200.2/32"},
				DNS:        []string{"fd86:ea04:1111::1", "1.1.1.1"},
				DNSSearch:  []string{"corp.example", "lab.example"},
				Peers: []core.WireGuardPeer{{
					PublicKey: keyB, Endpoint: "[2001:db8::1]:51820", AllowedIPs: []string{"::/0", "10.200.200.0/24"},
				}},
				AutoDefaultRoute: &tr,
			},
		},
		{
			name: "no default route leaves AutoDefaultRoute nil", file: "lan", in: "[Interface]\nPrivateKey=" + keyA + "\nAddress=10.0.0.2/24\n[Peer]\nPublicKey=" + keyB + "\nAllowedIPs=10.0.0.0/24\n",
			want: core.WireGuardSpec{
				Name: "lan", InterfaceName: "lan", PrivateKey: keyA, Addresses: []string{"10.0.0.2/24"},
				Peers: []core.WireGuardPeer{{PublicKey: keyB, AllowedIPs: []string{"10.0.0.0/24"}}},
			},
		},
		{
			name: "custom table is unsupported", file: "t", in: "[Interface]\nPrivateKey=" + keyA + "\nTable=1234\n[Peer]\nPublicKey=" + keyB + "\nAllowedIPs=0.0.0.0/0\n",
			want: core.WireGuardSpec{
				Name: "t", InterfaceName: "t", PrivateKey: keyA,
				Peers:            []core.WireGuardPeer{{PublicKey: keyB, AllowedIPs: []string{"0.0.0.0/0"}}},
				AutoDefaultRoute: &fa, Unsupported: []string{"Table = 1234"},
			},
		},
		{
			name: "case-insensitive keys, inline comments, no peers, fwmark off", file: "solo", in: "[interface]\nprivatekey = " + keyA + " # mine\nlistenport = 1\nfwmark = off\n",
			want: core.WireGuardSpec{Name: "solo", InterfaceName: "solo", PrivateKey: keyA, ListenPort: 1},
		},
		{
			name: "long name is truncated for the interface only", file: "a-very-long-profile-name-for-wireguard", in: "[Interface]\nPrivateKey=" + keyA + "\n",
			want: core.WireGuardSpec{Name: "a-very-long-profile-name-for-wireguard", InterfaceName: "a-very-long-pro", PrivateKey: keyA},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConf(tc.file, strings.NewReader(tc.in))
			if err != nil {
				t.Fatalf("ParseConf: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("spec mismatch\n got: %s\nwant: %s", dump(got), dump(tc.want))
			}
		})
	}
}

func dump(s core.WireGuardSpec) string {
	adr := "nil"
	if s.AutoDefaultRoute != nil {
		adr = fmt.Sprint(*s.AutoDefaultRoute)
	}
	s.AutoDefaultRoute = nil
	return fmt.Sprintf("%+v AutoDefaultRoute=%s", s, adr)
}

func TestParseConfErrors(t *testing.T) {
	cases := map[string]string{
		"empty":                    "",
		"no interface":             "[Peer]\nPublicKey=" + keyB + "\n",
		"no private key":           "[Interface]\nAddress=10.0.0.1/24\n",
		"key before section":       "PrivateKey=" + keyA + "\n[Interface]\n",
		"bad section":              "[Bogus]\nPrivateKey=" + keyA + "\n",
		"unterminated section":     "[Interface\nPrivateKey=" + keyA + "\n",
		"unknown interface key":    "[Interface]\nPrivateKey=" + keyA + "\nColour=blue\n",
		"unknown peer key":         "[Interface]\nPrivateKey=" + keyA + "\n[Peer]\nPublicKey=" + keyB + "\nRoute=1\n",
		"line without equals":      "[Interface]\nPrivateKey\n",
		"private key not base64":   "[Interface]\nPrivateKey=not*base64\n",
		"private key wrong length": "[Interface]\nPrivateKey=" + "AAAA" + "\n",
		"public key wrong length":  "[Interface]\nPrivateKey=" + keyA + "\n[Peer]\nPublicKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==\n",
		"peer without public key":  "[Interface]\nPrivateKey=" + keyA + "\n[Peer]\nAllowedIPs=0.0.0.0/0\n",
		"bad address":              "[Interface]\nPrivateKey=" + keyA + "\nAddress=10.0.0.999/24\n",
		"bad allowed ip":           "[Interface]\nPrivateKey=" + keyA + "\n[Peer]\nPublicKey=" + keyB + "\nAllowedIPs=example.com\n",
		"bad endpoint no port":     "[Interface]\nPrivateKey=" + keyA + "\n[Peer]\nPublicKey=" + keyB + "\nEndpoint=1.2.3.4\n",
		"bad endpoint port":        "[Interface]\nPrivateKey=" + keyA + "\n[Peer]\nPublicKey=" + keyB + "\nEndpoint=1.2.3.4:99999\n",
		"bad listen port":          "[Interface]\nPrivateKey=" + keyA + "\nListenPort=70000\n",
		"bad mtu":                  "[Interface]\nPrivateKey=" + keyA + "\nMTU=big\n",
		"bad fwmark":               "[Interface]\nPrivateKey=" + keyA + "\nFwMark=0x1ffffffff\n",
		"bad keepalive":            "[Interface]\nPrivateKey=" + keyA + "\n[Peer]\nPublicKey=" + keyB + "\nPersistentKeepalive=-1\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConf("x.conf", strings.NewReader(in))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrInvalidConf) || !IsInvalidConf(err) {
				t.Errorf("error %v does not wrap ErrInvalidConf", err)
			}
		})
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"wg0.conf": "wg0", "/etc/wireguard/office.conf": "office", `C:\wg\home.conf`: "home", "": "wg0", "  ": "wg0", "plain": "plain",
	} {
		if got := profileName(in); got != want {
			t.Errorf("profileName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"wg0": "wg0", "My VPN (US)": "MyVPNUS", "ünïcode": "ncode", "***": "wg0", "..": "wg0",
		"0123456789abcdefgh": "0123456789abcde",
	} {
		if got := ifaceName(in); got != want {
			t.Errorf("ifaceName(%q) = %q, want %q", in, got, want)
		}
	}
	if HasDefaultRoute(core.WireGuardSpec{Peers: []core.WireGuardPeer{{AllowedIPs: []string{"10.0.0.0/8"}}}}) {
		t.Error("HasDefaultRoute false positive")
	}
	if !HasDefaultRoute(core.WireGuardSpec{Peers: []core.WireGuardPeer{{AllowedIPs: []string{"::/0"}}}}) {
		t.Error("HasDefaultRoute false negative")
	}
}
