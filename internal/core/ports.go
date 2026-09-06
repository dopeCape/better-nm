package core

import "context"

// ChangeKind says which part of the world a backend saw change.
type ChangeKind string

const (
	ChangeStatus   ChangeKind = "status"
	ChangeDevices  ChangeKind = "devices"
	ChangeWifi     ChangeKind = "wifi"
	ChangeProfiles ChangeKind = "profiles"
	ChangeActive   ChangeKind = "active"
	ChangeVPN      ChangeKind = "vpn"
	ChangeMonitor  ChangeKind = "monitor"
)

// Change is a coarse invalidation hint; consumers re-read what they need.
type Change struct {
	Kind ChangeKind `json:"kind"`
	Path string     `json:"path,omitempty"` // backend object that changed, for debugging
}

// NetworkManager is the port the daemon uses to drive NM. internal/nm implements it
// over D-Bus; tests use a fake.
type NetworkManager interface {
	Status(ctx context.Context) (Status, error)
	Devices(ctx context.Context) ([]Device, error)
	// WifiNetworks lists SSIDs seen by device (or all Wi-Fi devices when device == "").
	WifiNetworks(ctx context.Context, device string) ([]WifiNetwork, error)
	// Scan requests a rescan; it returns once the request is accepted, not when results are in.
	Scan(ctx context.Context, device string) error
	Profiles(ctx context.Context) ([]Profile, error)
	Profile(ctx context.Context, uuid string) (Profile, error)
	ActiveConnections(ctx context.Context) ([]ActiveConnection, error)

	ConnectWifi(ctx context.Context, req ConnectWifiRequest) error
	// Activate activates a Profile; device may be "" to let NM choose.
	Activate(ctx context.Context, profileUUID, device string) error
	Deactivate(ctx context.Context, profileUUID string) error
	// DisconnectDevice brings a device down (Device.Disconnect); wired "off".
	DisconnectDevice(ctx context.Context, device string) error
	Forget(ctx context.Context, profileUUID string) error
	SetWifiEnabled(ctx context.Context, on bool) error
	SetAutoconnect(ctx context.Context, profileUUID string, on bool) error
	// UpdateIPConfig replaces the ipv4/ipv6 layer of a Profile (nil = leave as is).
	UpdateIPConfig(ctx context.Context, profileUUID string, ipv4, ipv6 *IPConfig) error
	// AddWireGuard creates an NM-native wireguard profile owned by the calling user.
	AddWireGuard(ctx context.Context, spec WireGuardSpec) (uuid string, err error)
	// ImportVPN imports a plugin file (openvpn .ovpn) via nmcli and returns the new UUID.
	ImportVPN(ctx context.Context, serviceKind, path string) (uuid string, err error)
	// SetProfilePermissions scopes a profile to the current user (prompt-free edits).
	SetProfilePermissions(ctx context.Context, profileUUID string, userOnly bool) error

	// Watch delivers Change hints until ctx ends. Implementations must not block on a slow consumer.
	Watch(ctx context.Context) (<-chan Change, error)
	Permissions(ctx context.Context) (map[string]string, error)
}

// VPNAdapter is one backend's view of the VPNs it owns.
type VPNAdapter interface {
	Backend() VPNBackend
	List(ctx context.Context) ([]VPN, error)
	Connect(ctx context.Context, id string) error
	Disconnect(ctx context.Context, id string) error
	// Watch delivers a hint whenever any VPN of this backend changed.
	Watch(ctx context.Context) (<-chan Change, error)
}

// Store persists monitoring history and settings.
type Store interface {
	AddSample(ctx context.Context, s Sample) error
	// Samples returns samples for key/anchor, newest last, at most limit (0 = all).
	Samples(ctx context.Context, networkKey, anchor string, limit int) ([]Sample, error)
	Baselines(ctx context.Context, networkKey string) ([]Baseline, error)
	PutBaseline(ctx context.Context, b Baseline) error
	DeleteBaselines(ctx context.Context, networkKey string) error
	AddSpeedResult(ctx context.Context, r SpeedResult) error
	SpeedResults(ctx context.Context, networkKey string, limit int) ([]SpeedResult, error)
	AddEvent(ctx context.Context, e Event) error
	Events(ctx context.Context, limit int) ([]Event, error)
	// Prune drops samples older than the retention window.
	Prune(ctx context.Context) error
	Close() error
}

// Notifier delivers notable events to the user.
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}

// TailscaleControl is the optional extra surface the Tailscale adapter offers.
// The daemon type-asserts a VPNAdapter to it.
type TailscaleControl interface {
	// SetExitNode selects a peer (by ID or name; "" clears) and enables it.
	SetExitNode(ctx context.Context, peer string, allowLAN bool) error
	UseExitNode(ctx context.Context, on bool) error
	SetAcceptDNS(ctx context.Context, on bool) error
	// Login starts an interactive login and returns the URL to open.
	Login(ctx context.Context) (url string, err error)
	Logout(ctx context.Context) error
}

// SpeedTester runs an on-demand bandwidth test.
type SpeedTester interface {
	Run(ctx context.Context, opts SpeedOptions, progress func(SpeedProgress)) (SpeedResult, error)
}

// SpeedOptions selects the provider and bounds the test.
type SpeedOptions struct {
	Provider   string `json:"provider,omitempty"` // cloudflare (default) | iperf3 | librespeed
	Server     string `json:"server,omitempty"`   // iperf3 host[:port] or librespeed base URL
	Quick      bool   `json:"quick,omitempty"`    // ~10% of the bytes
	MaxBytes   int64  `json:"max_bytes,omitempty"`
	NetworkKey string `json:"network_key,omitempty"`
}
