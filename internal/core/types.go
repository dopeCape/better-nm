// Package core holds bnm's domain model: the vocabulary every other package
// speaks. It has no dependencies on D-Bus, netlink, SQLite or any UI toolkit.
// See CONTEXT.md at the repo root for the glossary these types implement.
package core

import "time"

// DeviceKind is what a Device physically or logically is.
type DeviceKind string

const (
	DeviceWifi      DeviceKind = "wifi"
	DeviceEthernet  DeviceKind = "ethernet"
	DeviceTun       DeviceKind = "tun"
	DeviceBridge    DeviceKind = "bridge"
	DeviceVeth      DeviceKind = "veth"
	DeviceVlan      DeviceKind = "vlan"
	DeviceWireGuard DeviceKind = "wireguard"
	DeviceLoopback  DeviceKind = "loopback"
	DeviceOther     DeviceKind = "other"
)

// DeviceClass separates hardware the user cares about from plumbing.
type DeviceClass string

const (
	ClassPhysical DeviceClass = "physical" // has a sysfs device link
	ClassInfra    DeviceClass = "infra"    // bridge/veth/vlan/tun owned by a runtime
	ClassLoopback DeviceClass = "loopback"
)

// DeviceState is bnm's simplified view of NM's device state.
type DeviceState string

const (
	DeviceUnmanaged    DeviceState = "unmanaged"
	DeviceUnavailable  DeviceState = "unavailable"
	DeviceDisconnected DeviceState = "disconnected"
	DeviceConnecting   DeviceState = "connecting"
	DeviceConnected    DeviceState = "connected"
	DeviceExternal     DeviceState = "external" // up, but configured outside NM
	DeviceFailed       DeviceState = "failed"
)

// Device is a network interface the host has.
type Device struct {
	Name       string      `json:"name"`
	IfIndex    int         `json:"ifindex"`
	Kind       DeviceKind  `json:"kind"`
	Class      DeviceClass `json:"class"`
	State      DeviceState `json:"state"`
	Managed    bool        `json:"managed"`
	Owner      string      `json:"owner,omitempty"` // docker, podman, libvirt, incus, nspawn, k8s, tailscale, veth
	HwAddr     string      `json:"hwaddr,omitempty"`
	Driver     string      `json:"driver,omitempty"`
	IPv4       []string    `json:"ipv4,omitempty"` // CIDR
	IPv6       []string    `json:"ipv6,omitempty"` // CIDR
	Gateway4   string      `json:"gateway4,omitempty"`
	DNS        []string    `json:"dns,omitempty"`
	ActiveUUID string      `json:"active_uuid,omitempty"` // Profile UUID currently active on this device
	ActiveName string      `json:"active_name,omitempty"`
	Speed      int         `json:"speed_mbps,omitempty"` // wired link speed, wifi bitrate
	NMPath     string      `json:"nm_path,omitempty"`
}

// WifiSecurity is the security family of a Wi-Fi network.
type WifiSecurity string

const (
	SecOpen   WifiSecurity = "open"
	SecOWE    WifiSecurity = "owe"
	SecWEP    WifiSecurity = "wep"
	SecWPAPSK WifiSecurity = "wpa-psk"
	SecSAE    WifiSecurity = "sae" // WPA3 personal
	SecWPAEAP WifiSecurity = "wpa-eap"
)

// WifiNetwork is an SSID as seen by a Wi-Fi device, aggregated over its access points.
type WifiNetwork struct {
	SSID        string       `json:"ssid"`
	Device      string       `json:"device"`
	Strength    uint8        `json:"strength"` // 0-100, best AP
	Security    WifiSecurity `json:"security"`
	Frequency   uint32       `json:"frequency_mhz"` // best AP
	Channel     int          `json:"channel"`
	Band        string       `json:"band"` // 2.4, 5, 6
	BSSIDs      []string     `json:"bssids"`
	Known       bool         `json:"known"` // a Profile exists
	ProfileUUID string       `json:"profile_uuid,omitempty"`
	Active      bool         `json:"active"`
	Hidden      bool         `json:"hidden,omitempty"`
	LastSeen    time.Time    `json:"last_seen,omitempty"`
}

// IPMethod mirrors NM's ipv4/ipv6 method values.
type IPMethod string

const (
	IPAuto      IPMethod = "auto"
	IPManual    IPMethod = "manual"
	IPDisabled  IPMethod = "disabled"
	IPLinkLocal IPMethod = "link-local"
	IPShared    IPMethod = "shared"
	IPIgnore    IPMethod = "ignore"
)

// IPConfig is the editable IP layer of a Profile, for one address family.
type IPConfig struct {
	Method        IPMethod `json:"method"`
	Addresses     []string `json:"addresses,omitempty"` // CIDR
	Gateway       string   `json:"gateway,omitempty"`
	DNS           []string `json:"dns,omitempty"`
	DNSSearch     []string `json:"dns_search,omitempty"`
	IgnoreAutoDNS bool     `json:"ignore_auto_dns,omitempty"`
	NeverDefault  bool     `json:"never_default,omitempty"`
}

// ProfileType is NM's connection.type, normalised.
type ProfileType string

const (
	ProfileWifi      ProfileType = "wifi"
	ProfileEthernet  ProfileType = "ethernet"
	ProfileWireGuard ProfileType = "wireguard"
	ProfileVPN       ProfileType = "vpn" // NM plugin VPN; see VPNServiceType
	ProfileBridge    ProfileType = "bridge"
	ProfileOther     ProfileType = "other"
)

// Profile is a saved NetworkManager connection.
type Profile struct {
	UUID           string       `json:"uuid"`
	Name           string       `json:"name"` // connection.id
	Type           ProfileType  `json:"type"`
	RawType        string       `json:"raw_type"` // NM connection.type verbatim
	InterfaceName  string       `json:"interface_name,omitempty"`
	Autoconnect    bool         `json:"autoconnect"`
	SSID           string       `json:"ssid,omitempty"`
	Security       WifiSecurity `json:"security,omitempty"`
	VPNServiceType string       `json:"vpn_service_type,omitempty"` // e.g. org.freedesktop.NetworkManager.openvpn
	Timestamp      time.Time    `json:"timestamp,omitempty"`        // last activated
	IPv4           IPConfig     `json:"ipv4"`
	IPv6           IPConfig     `json:"ipv6"`
	Permissions    []string     `json:"permissions,omitempty"` // NM connection.permissions
	Filename       string       `json:"filename,omitempty"`
	VersionID      uint64       `json:"version_id"`
	Active         bool         `json:"active"`
	NMPath         string       `json:"nm_path,omitempty"`
}

// ActiveState is NM's ActiveConnection state, normalised.
type ActiveState string

const (
	ActiveActivating   ActiveState = "activating"
	ActiveActivated    ActiveState = "activated"
	ActiveDeactivating ActiveState = "deactivating"
	ActiveDeactivated  ActiveState = "deactivated"
	ActiveUnknown      ActiveState = "unknown"
)

// ActiveConnection is a Profile currently activated on one or more Devices.
type ActiveConnection struct {
	Path        string      `json:"path"`
	ProfileUUID string      `json:"profile_uuid"`
	ProfileName string      `json:"profile_name"`
	Type        ProfileType `json:"type"`
	Devices     []string    `json:"devices"`
	State       ActiveState `json:"state"`
	Default4    bool        `json:"default4"`
	Default6    bool        `json:"default6"`
	VPN         bool        `json:"vpn"`
	VPNState    string      `json:"vpn_state,omitempty"` // NM VpnState name for vpn profiles
	VPNBanner   string      `json:"vpn_banner,omitempty"`
	IPv4        []string    `json:"ipv4,omitempty"`
	IPv6        []string    `json:"ipv6,omitempty"`
	Gateway4    string      `json:"gateway4,omitempty"`
	DNS         []string    `json:"dns,omitempty"`
	External    bool        `json:"external"` // configured outside NM (docker0, tailscale0)
}

// Connectivity is NM's connectivity check result.
type Connectivity string

const (
	ConnUnknown Connectivity = "unknown"
	ConnNone    Connectivity = "none"
	ConnPortal  Connectivity = "portal"
	ConnLimited Connectivity = "limited"
	ConnFull    Connectivity = "full"
)

// Status is the one-screen summary.
type Status struct {
	NMState      string            `json:"nm_state"` // NM global state name
	Connectivity Connectivity      `json:"connectivity"`
	Networking   bool              `json:"networking"`
	WifiEnabled  bool              `json:"wifi_enabled"`
	WifiHardware bool              `json:"wifi_hardware"`
	Primary      *ActiveConnection `json:"primary,omitempty"`
	NetworkKey   string            `json:"network_key,omitempty"` // of the primary connection
	NMVersion    string            `json:"nm_version"`
	Permissions  map[string]string `json:"permissions,omitempty"` // polkit action -> yes|auth|no
}

// NetworkKey derives the monitoring identity of an active connection.
// wifi:<ssid> for Wi-Fi profiles, wired:<uuid> otherwise, "" when nothing is primary.
func NetworkKey(primary *ActiveConnection, profiles []Profile) string {
	if primary == nil {
		return ""
	}
	for _, p := range profiles {
		if p.UUID == primary.ProfileUUID {
			if p.Type == ProfileWifi && p.SSID != "" {
				return "wifi:" + p.SSID
			}
			return string(p.Type) + ":" + p.UUID
		}
	}
	return "conn:" + primary.ProfileUUID
}

// ConnectWifiRequest joins a Wi-Fi network, creating a Profile when none exists.
type ConnectWifiRequest struct {
	Device   string `json:"device,omitempty"` // "" = first Wi-Fi device
	SSID     string `json:"ssid"`
	Password string `json:"password,omitempty"`
	Hidden   bool   `json:"hidden,omitempty"`
	// Username is for WPA-EAP networks; v1 supports PEAP/MSCHAPv2 only.
	Username string `json:"username,omitempty"`
}

// WireGuardPeer is one [Peer] section of a WireGuard profile.
type WireGuardPeer struct {
	PublicKey           string   `json:"public_key"`
	PresharedKey        string   `json:"preshared_key,omitempty"`
	Endpoint            string   `json:"endpoint,omitempty"`
	AllowedIPs          []string `json:"allowed_ips"`
	PersistentKeepalive int      `json:"persistent_keepalive,omitempty"`
}

// WireGuardSpec creates an NM-native WireGuard profile (from a wg-quick .conf or fields).
type WireGuardSpec struct {
	Name          string          `json:"name"`
	InterfaceName string          `json:"interface_name"`
	PrivateKey    string          `json:"private_key"`
	ListenPort    int             `json:"listen_port,omitempty"`
	FwMark        int             `json:"fwmark,omitempty"`
	MTU           int             `json:"mtu,omitempty"`
	Addresses     []string        `json:"addresses"` // CIDR, v4 and v6 mixed
	DNS           []string        `json:"dns,omitempty"`
	DNSSearch     []string        `json:"dns_search,omitempty"`
	Peers         []WireGuardPeer `json:"peers"`
	// AutoDefaultRoute mirrors wg-quick Table=auto: when a peer allows 0.0.0.0/0 NM adds
	// policy routing so the tunnel becomes the default route. Nil = NM default.
	AutoDefaultRoute *bool `json:"auto_default_route,omitempty"`
	Autoconnect      bool  `json:"autoconnect"`
	// Unsupported lists wg-quick keys the importer had to drop (PreUp, PostUp, ...).
	Unsupported []string `json:"unsupported,omitempty"`
}

// Permission values returned by NM GetPermissions.
const (
	PermYes  = "yes"
	PermAuth = "auth"
	PermNo   = "no"
)
