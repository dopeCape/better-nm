package core

import "time"

// VPNBackend identifies which adapter owns a VPN.
type VPNBackend string

const (
	BackendTailscale VPNBackend = "tailscale"
	BackendWireGuard VPNBackend = "wireguard" // NM-native wireguard profile
	BackendNMVPN     VPNBackend = "nm-vpn"    // generic NM plugin VPN (OpenVPN, strongSwan, ...)
)

// VPNState is the common state machine every backend maps onto.
type VPNState string

const (
	VPNDisconnected VPNState = "disconnected"
	VPNConnecting   VPNState = "connecting"
	VPNConnected    VPNState = "connected"
	VPNError        VPNState = "error"
	// VPNNeedsSetup: the backend exists but bnm cannot drive it (Tailscale without
	// operator mode, a profile the user may not activate). Detail says what to do.
	VPNNeedsSetup VPNState = "needs-setup"
	// VPNNeedsAuth: a login URL or secret prompt is outstanding. AuthURL may be set.
	VPNNeedsAuth VPNState = "needs-auth"
	// VPNUnavailable: backend not installed / daemon not running.
	VPNUnavailable VPNState = "unavailable"
)

// VPN is one thing the user toggles, whatever the backend.
type VPN struct {
	// ID is stable across restarts: "tailscale" for Tailscale, the profile UUID otherwise.
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Backend VPNBackend `json:"backend"`
	// Kind is the human label: Tailscale, WireGuard, OpenVPN, IPsec (strongSwan), L2TP, SSTP, PPTP...
	Kind    string    `json:"kind"`
	State   VPNState  `json:"state"`
	Detail  string    `json:"detail,omitempty"` // human hint, e.g. how to fix needs-setup
	Error   string    `json:"error,omitempty"`
	AuthURL string    `json:"auth_url,omitempty"`
	Since   time.Time `json:"since,omitempty"` // when the current state began, if known
	// Writable says whether bnm can change this VPN's state right now.
	Writable bool `json:"writable"`

	Tailscale *TailscaleInfo `json:"tailscale,omitempty"`
	WireGuard *WireGuardInfo `json:"wireguard,omitempty"`
	NMVPN     *NMVPNInfo     `json:"nm_vpn,omitempty"`
}

// TailscaleInfo carries the Tailscale-only extras.
type TailscaleInfo struct {
	BackendState string          `json:"backend_state"` // ipn.State name
	Version      string          `json:"version,omitempty"`
	ControlURL   string          `json:"control_url,omitempty"` // Headscale shows here
	Tailnet      string          `json:"tailnet,omitempty"`
	MagicDNS     string          `json:"magic_dns_suffix,omitempty"`
	SelfIPs      []string        `json:"self_ips,omitempty"`
	SelfName     string          `json:"self_name,omitempty"`
	OperatorOK   bool            `json:"operator_ok"` // writes allowed for this uid
	ExitNodeID   string          `json:"exit_node_id,omitempty"`
	ExitNodeName string          `json:"exit_node_name,omitempty"`
	ExitNodeOn   bool            `json:"exit_node_on"`
	AllowLAN     bool            `json:"exit_node_allow_lan"`
	AcceptDNS    bool            `json:"accept_dns"`
	Peers        []TailscalePeer `json:"peers,omitempty"`
	Health       []string        `json:"health,omitempty"`
}

// TailscalePeer is one node in the tailnet.
type TailscalePeer struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"` // DNS name without the suffix
	HostName       string    `json:"hostname,omitempty"`
	OS             string    `json:"os,omitempty"`
	IPs            []string  `json:"ips"`
	Online         bool      `json:"online"`
	ExitNode       bool      `json:"exit_node"`        // currently used as exit node
	ExitNodeOption bool      `json:"exit_node_option"` // offers exit node
	Relay          string    `json:"relay,omitempty"`
	LastSeen       time.Time `json:"last_seen,omitempty"`
}

// WireGuardInfo carries the WireGuard-only extras (NM Device.WireGuard + profile peers).
type WireGuardInfo struct {
	InterfaceName string          `json:"interface_name"`
	PublicKey     string          `json:"public_key,omitempty"`
	ListenPort    int             `json:"listen_port,omitempty"`
	Peers         []WireGuardPeer `json:"peers,omitempty"`
	Addresses     []string        `json:"addresses,omitempty"`
	// Runtime stats come from the kernel via netlink when the interface is up; may be zero.
	LastHandshake time.Time `json:"last_handshake,omitempty"`
	RxBytes       uint64    `json:"rx_bytes,omitempty"`
	TxBytes       uint64    `json:"tx_bytes,omitempty"`
}

// NMVPNInfo carries generic NM plugin VPN extras.
type NMVPNInfo struct {
	ServiceType string `json:"service_type"`
	Gateway     string `json:"gateway,omitempty"` // remote / gateway from vpn.data, if known
	Username    string `json:"username,omitempty"`
	Banner      string `json:"banner,omitempty"`
	NMVpnState  string `json:"nm_vpn_state,omitempty"`
}

// VPNKindForServiceType maps an NM vpn.service-type to a display Kind.
func VPNKindForServiceType(st string) string {
	switch st {
	case "org.freedesktop.NetworkManager.openvpn":
		return "OpenVPN"
	case "org.freedesktop.NetworkManager.strongswan":
		return "IPsec (strongSwan)"
	case "org.freedesktop.NetworkManager.libreswan":
		return "IPsec (Libreswan)"
	case "org.freedesktop.NetworkManager.l2tp":
		return "L2TP"
	case "org.freedesktop.NetworkManager.sstp":
		return "SSTP"
	case "org.freedesktop.NetworkManager.pptp":
		return "PPTP"
	case "org.freedesktop.NetworkManager.openconnect":
		return "OpenConnect"
	case "org.freedesktop.NetworkManager.vpnc":
		return "vpnc"
	case "org.freedesktop.NetworkManager.fortisslvpn":
		return "Fortinet SSL VPN"
	case "org.freedesktop.NetworkManager.protun":
		return "Proton VPN"
	case "":
		return "VPN"
	}
	return "VPN (" + st + ")"
}
