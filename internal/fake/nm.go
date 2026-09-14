// Package fake holds in-memory doubles of bnm's ports: a NetworkManager with a
// seeded, mutable world, VPN adapters, a Store, a Notifier, a Monitor, a
// SpeedTester and Diag sources. The daemon, API and client tests run against
// them, and `bnmd --fake` serves them so surfaces can be developed without a
// real NetworkManager. Every mutator pushes a core.Change to Watch subscribers,
// exactly as the real adapters would.
package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Seeded object identifiers, exported so tests can refer to them.
const (
	WifiDevice   = "wlan0"
	WiredDevice  = "eth0"
	HomeSSID     = "HomeNet"
	HomeUUID     = "11111111-1111-4111-8111-111111111111"
	OfficeSSID   = "Office"
	OfficeUUID   = "22222222-2222-4222-8222-222222222222"
	WiredUUID    = "33333333-3333-4333-8333-333333333333"
	CafeSSID     = "CoffeeShop"
	NeighbourSSD = "Neighbour5G"
	// WrongPassword is a password a prompting NM (UseSecrets) rejects, so
	// the retry prompt (RequestNew) can be exercised.
	WrongPassword = "wrong"
)

// NM is an in-memory core.NetworkManager.
type NM struct {
	mu          sync.Mutex
	devices     []core.Device
	aps         map[string][]core.WifiNetwork // device -> networks
	profiles    []core.Profile
	active      []core.ActiveConnection
	connect     core.Connectivity
	networking  bool
	wifiEnabled bool
	wifiHW      bool
	nmState     string
	perms       map[string]string
	fail        map[string]error
	nextUUID    int
	watchers    []chan core.Change
	// secrets, when set, prompts for a missing Wi-Fi password the way the NM
	// agent does instead of failing (UseSecrets).
	secrets *SecretBroker
}

// NewNM returns a seeded world: one Wi-Fi device (4 SSIDs, HomeNet active and
// primary), one wired device (disconnected), three profiles, connectivity full.
func NewNM() *NM {
	n := &NM{
		aps:         map[string][]core.WifiNetwork{},
		connect:     core.ConnFull,
		networking:  true,
		wifiEnabled: true,
		wifiHW:      true,
		nmState:     "connected-global",
		perms: map[string]string{
			"org.freedesktop.NetworkManager.enable-disable-wifi":         core.PermYes,
			"org.freedesktop.NetworkManager.settings.modify.own":         core.PermYes,
			"org.freedesktop.NetworkManager.settings.modify.system":      core.PermAuth,
			"org.freedesktop.NetworkManager.network-control":             core.PermYes,
			"org.freedesktop.NetworkManager.wifi.scan":                   core.PermYes,
			"org.freedesktop.NetworkManager.enable-disable-connectivity": core.PermAuth,
		},
		fail: map[string]error{},
	}
	n.devices = []core.Device{
		{Name: WifiDevice, IfIndex: 3, Kind: core.DeviceWifi, Class: core.ClassPhysical, State: core.DeviceConnected, Managed: true,
			HwAddr: "aa:bb:cc:dd:ee:01", Driver: "iwlwifi", IPv4: []string{"192.168.1.42/24"}, Gateway4: "192.168.1.1",
			DNS: []string{"192.168.1.1"}, ActiveUUID: HomeUUID, ActiveName: HomeSSID, Speed: 866, NMPath: "/org/freedesktop/NetworkManager/Devices/3"},
		{Name: WiredDevice, IfIndex: 2, Kind: core.DeviceEthernet, Class: core.ClassPhysical, State: core.DeviceDisconnected, Managed: true,
			HwAddr: "aa:bb:cc:dd:ee:02", Driver: "e1000e", NMPath: "/org/freedesktop/NetworkManager/Devices/2"},
	}
	n.aps[WifiDevice] = []core.WifiNetwork{
		{SSID: HomeSSID, Device: WifiDevice, Strength: 82, Security: core.SecWPAPSK, Frequency: 5180, Channel: 36, Band: "5",
			BSSIDs: []string{"10:00:00:00:00:01", "10:00:00:00:00:02"}, Known: true, ProfileUUID: HomeUUID, Active: true},
		{SSID: CafeSSID, Device: WifiDevice, Strength: 55, Security: core.SecOpen, Frequency: 2437, Channel: 6, Band: "2.4",
			BSSIDs: []string{"20:00:00:00:00:01"}},
		{SSID: NeighbourSSD, Device: WifiDevice, Strength: 31, Security: core.SecSAE, Frequency: 5745, Channel: 149, Band: "5",
			BSSIDs: []string{"30:00:00:00:00:01"}},
		{SSID: OfficeSSID, Device: WifiDevice, Strength: 20, Security: core.SecWPAEAP, Frequency: 5220, Channel: 44, Band: "5",
			BSSIDs: []string{"40:00:00:00:00:01"}, Known: true, ProfileUUID: OfficeUUID},
	}
	now := time.Now()
	n.profiles = []core.Profile{
		{UUID: HomeUUID, Name: HomeSSID, Type: core.ProfileWifi, RawType: "802-11-wireless", Autoconnect: true, SSID: HomeSSID, Security: core.SecWPAPSK,
			Timestamp: now.Add(-time.Hour), IPv4: core.IPConfig{Method: core.IPAuto}, IPv6: core.IPConfig{Method: core.IPAuto}, VersionID: 1, Active: true},
		{UUID: OfficeUUID, Name: OfficeSSID, Type: core.ProfileWifi, RawType: "802-11-wireless", Autoconnect: true, SSID: OfficeSSID, Security: core.SecWPAEAP,
			Timestamp: now.Add(-72 * time.Hour), IPv4: core.IPConfig{Method: core.IPAuto}, IPv6: core.IPConfig{Method: core.IPAuto}, VersionID: 1},
		{UUID: WiredUUID, Name: "Wired connection 1", Type: core.ProfileEthernet, RawType: "802-3-ethernet", Autoconnect: true, InterfaceName: WiredDevice,
			IPv4: core.IPConfig{Method: core.IPAuto}, IPv6: core.IPConfig{Method: core.IPAuto}, VersionID: 1},
	}
	n.active = []core.ActiveConnection{{
		Path: "/org/freedesktop/NetworkManager/ActiveConnection/1", ProfileUUID: HomeUUID, ProfileName: HomeSSID, Type: core.ProfileWifi,
		Devices: []string{WifiDevice}, State: core.ActiveActivated, Default4: true, Default6: false,
		IPv4: []string{"192.168.1.42/24"}, Gateway4: "192.168.1.1", DNS: []string{"192.168.1.1"},
	}}
	return n
}

// Fail makes the named method (e.g. "Activate", "ConnectWifi", "Scan") return err
// until cleared with Fail(method, nil). Used to test error mapping.
func (n *NM) Fail(method string, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err == nil {
		delete(n.fail, method)
		return
	}
	n.fail[method] = err
}

func (n *NM) failing(method string) error {
	if err, ok := n.fail[method]; ok {
		return err
	}
	return nil
}

// --- core.NetworkManager -------------------------------------------------

func (n *NM) Status(ctx context.Context) (core.Status, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Status"); err != nil {
		return core.Status{}, err
	}
	return n.statusLocked(), nil
}

func (n *NM) statusLocked() core.Status {
	s := core.Status{
		NMState:      n.nmState,
		Connectivity: n.connect,
		Networking:   n.networking,
		WifiEnabled:  n.wifiEnabled,
		WifiHardware: n.wifiHW,
		NMVersion:    "1.46.0-fake",
		Permissions:  copyMap(n.perms),
	}
	if p := n.primaryLocked(); p != nil {
		cp := *p
		s.Primary = &cp
		s.NetworkKey = core.NetworkKey(&cp, n.profiles)
	}
	return s
}

func (n *NM) primaryLocked() *core.ActiveConnection {
	for i := range n.active {
		a := &n.active[i]
		if a.VPN || a.Type == core.ProfileVPN || a.Type == core.ProfileWireGuard {
			continue
		}
		if a.Default4 || a.Default6 {
			return a
		}
	}
	return nil
}

func (n *NM) Devices(ctx context.Context) ([]core.Device, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Devices"); err != nil {
		return nil, err
	}
	return append([]core.Device(nil), n.devices...), nil
}

func (n *NM) WifiNetworks(ctx context.Context, device string) ([]core.WifiNetwork, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("WifiNetworks"); err != nil {
		return nil, err
	}
	if device != "" {
		if _, ok := n.aps[device]; !ok {
			return nil, core.Errorf(core.KindNotFound, "", "nm: no Wi-Fi device %q", device)
		}
	}
	var out []core.WifiNetwork
	for dev, nets := range n.aps {
		if device != "" && dev != device {
			continue
		}
		for _, w := range nets {
			w.Active = false
			for _, a := range n.active {
				if a.ProfileUUID == w.ProfileUUID && w.ProfileUUID != "" && a.State == core.ActiveActivated {
					w.Active = true
				}
			}
			out = append(out, w)
		}
	}
	return out, nil
}

func (n *NM) Scan(ctx context.Context, device string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Scan"); err != nil {
		return err
	}
	if device == "" {
		device = WifiDevice
	}
	if _, ok := n.aps[device]; !ok {
		return core.Errorf(core.KindNotFound, "", "nm: no Wi-Fi device %q", device)
	}
	// A scan in the fake nudges the signal strengths so callers see something change.
	for i := range n.aps[device] {
		w := &n.aps[device][i]
		if w.Strength > 5 {
			w.Strength--
		}
		w.LastSeen = time.Now()
	}
	n.emitLocked(core.Change{Kind: core.ChangeWifi, Path: device})
	return nil
}

func (n *NM) Profiles(ctx context.Context) ([]core.Profile, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Profiles"); err != nil {
		return nil, err
	}
	out := make([]core.Profile, len(n.profiles))
	for i, p := range n.profiles {
		p.Active = n.isActiveLocked(p.UUID)
		out[i] = p
	}
	return out, nil
}

func (n *NM) Profile(ctx context.Context, uuid string) (core.Profile, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Profile"); err != nil {
		return core.Profile{}, err
	}
	i := n.profileIndexLocked(uuid)
	if i < 0 {
		return core.Profile{}, core.Errorf(core.KindNotFound, "", "nm: profile %s not found", uuid)
	}
	p := n.profiles[i]
	p.Active = n.isActiveLocked(uuid)
	return p, nil
}

func (n *NM) ActiveConnections(ctx context.Context) ([]core.ActiveConnection, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("ActiveConnections"); err != nil {
		return nil, err
	}
	return append([]core.ActiveConnection(nil), n.active...), nil
}

// UseSecrets makes ConnectWifi prompt through b when a secured, unknown
// network is joined without a password or with WrongPassword (the latter as
// a RequestNew retry), and again, marked RequestNew, when an answer is shorter
// than the 8 characters WPA needs; this mirrors the real agent path so
// surfaces can be exercised against `bnmd --fake`.
func (n *NM) UseSecrets(b *SecretBroker) {
	n.mu.Lock()
	n.secrets = b
	n.mu.Unlock()
}

// askPassword raises a psk request for ssid and waits for the answer. Called
// with the mutex released.
func (n *NM) askPassword(ctx context.Context, b *SecretBroker, ssid, uuid string, retry bool) (string, error) {
	req := core.SecretRequest{
		ConnectionUUID: uuid, ConnectionName: ssid, SSID: ssid, SettingName: "802-11-wireless-security",
		Fields:     []core.SecretField{{Key: "psk", Label: "Wi-Fi password", Secret: true}},
		RequestNew: retry, UserRequested: true,
	}
	ch := b.Raise(req)
	select {
	case <-ctx.Done():
		return "", core.Errorf(core.KindInternal, "", "nm: connect %s: %v", ssid, ctx.Err())
	case a, ok := <-ch:
		if !ok {
			return "", core.Errorf(core.KindInvalid, "the password prompt was cancelled or not answered in time; retry, or pass --password", "nm: %s: no secrets available", ssid)
		}
		return a.Secrets["psk"], nil
	}
}

func (n *NM) ConnectWifi(ctx context.Context, req core.ConnectWifiRequest) error {
	n.mu.Lock()
	if n.secrets != nil && (req.Password == "" || req.Password == WrongPassword) && !req.Hidden {
		dev := req.Device
		if dev == "" {
			dev = WifiDevice
		}
		var net *core.WifiNetwork
		for i := range n.aps[dev] {
			if n.aps[dev][i].SSID == req.SSID {
				net = &n.aps[dev][i]
			}
		}
		if net != nil && !net.Known && (net.Security == core.SecWPAPSK || net.Security == core.SecSAE) && n.failing("ConnectWifi") == nil {
			b := n.secrets
			n.mu.Unlock()
			uuid := fmt.Sprintf("prompt-%s", req.SSID)
			rejected := req.Password == WrongPassword
			req.Password = ""
			for round := 0; round < 3; round++ {
				pw, err := n.askPassword(ctx, b, req.SSID, uuid, rejected || round > 0)
				if err != nil {
					return err
				}
				if len(pw) >= 8 {
					req.Password = pw
					break
				}
			}
			if req.Password == "" {
				return core.Errorf(core.KindInvalid, "check the password and try again", "nm: connect %s: wrong password", req.SSID)
			}
			n.mu.Lock()
		}
	}
	defer n.mu.Unlock()
	if err := n.failing("ConnectWifi"); err != nil {
		return err
	}
	if req.SSID == "" {
		return core.Errorf(core.KindInvalid, "", "nm: ssid is required")
	}
	dev := req.Device
	if dev == "" {
		dev = WifiDevice
	}
	if _, ok := n.aps[dev]; !ok {
		return core.Errorf(core.KindNotFound, "", "nm: no Wi-Fi device %q", dev)
	}
	if !n.wifiEnabled {
		return core.Errorf(core.KindUnavailable, "run `bnm wifi on`", "nm: Wi-Fi is disabled")
	}
	var net *core.WifiNetwork
	for i := range n.aps[dev] {
		if n.aps[dev][i].SSID == req.SSID {
			net = &n.aps[dev][i]
		}
	}
	if net == nil && !req.Hidden {
		return core.Errorf(core.KindNotFound, "run `bnm wifi scan` and retry", "nm: network %q not in range", req.SSID)
	}
	sec := core.SecOpen
	if net != nil {
		sec = net.Security
	} else if req.Password != "" {
		sec = core.SecWPAPSK
	}
	if (sec == core.SecWPAPSK || sec == core.SecSAE) && req.Password == "" && (net == nil || !net.Known) {
		return core.Errorf(core.KindInvalid, "", "nm: %q needs a password", req.SSID)
	}
	uuid := ""
	if net != nil && net.Known {
		uuid = net.ProfileUUID
	} else {
		uuid = n.newUUIDLocked()
		n.profiles = append(n.profiles, core.Profile{
			UUID: uuid, Name: req.SSID, Type: core.ProfileWifi, RawType: "802-11-wireless", Autoconnect: true,
			SSID: req.SSID, Security: sec, IPv4: core.IPConfig{Method: core.IPAuto}, IPv6: core.IPConfig{Method: core.IPAuto},
			VersionID: 1, Permissions: []string{"user:fake"},
		})
		if net != nil {
			net.Known = true
			net.ProfileUUID = uuid
		} else {
			n.aps[dev] = append(n.aps[dev], core.WifiNetwork{SSID: req.SSID, Device: dev, Strength: 40, Security: sec,
				Frequency: 2412, Channel: 1, Band: "2.4", Known: true, ProfileUUID: uuid, Hidden: true})
		}
		n.emitLocked(core.Change{Kind: core.ChangeProfiles, Path: uuid})
	}
	n.activateLocked(uuid, dev)
	return nil
}

func (n *NM) Activate(ctx context.Context, profileUUID, device string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Activate"); err != nil {
		return err
	}
	i := n.profileIndexLocked(profileUUID)
	if i < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: profile %s not found", profileUUID)
	}
	p := n.profiles[i]
	if device == "" {
		switch p.Type {
		case core.ProfileWifi:
			device = WifiDevice
		case core.ProfileEthernet:
			device = WiredDevice
		default:
			device = p.InterfaceName
		}
	}
	if device != "" && p.Type != core.ProfileVPN && p.Type != core.ProfileWireGuard && n.deviceIndexLocked(device) < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: device %q not found", device)
	}
	n.activateLocked(profileUUID, device)
	return nil
}

func (n *NM) activateLocked(uuid, device string) {
	p := n.profiles[n.profileIndexLocked(uuid)]
	isVPN := p.Type == core.ProfileVPN || p.Type == core.ProfileWireGuard
	// One layer-2 connection per device: drop whatever the device carried before.
	if !isVPN {
		n.removeActiveLocked(func(a core.ActiveConnection) bool {
			return !a.VPN && a.Type != core.ProfileWireGuard && contains(a.Devices, device)
		})
	} else {
		n.removeActiveLocked(func(a core.ActiveConnection) bool { return a.ProfileUUID == uuid })
	}
	n.nextUUID++
	ac := core.ActiveConnection{
		Path:        fmt.Sprintf("/org/freedesktop/NetworkManager/ActiveConnection/%d", 100+n.nextUUID),
		ProfileUUID: uuid, ProfileName: p.Name, Type: p.Type, State: core.ActiveActivated,
		VPN: p.Type == core.ProfileVPN,
	}
	if device != "" {
		ac.Devices = []string{device}
	}
	if !isVPN {
		ac.Default4 = true
		ip, gw := fakeAddress(uuid)
		ac.IPv4 = []string{ip}
		ac.Gateway4 = gw
		ac.DNS = []string{gw}
		if di := n.deviceIndexLocked(device); di >= 0 {
			d := &n.devices[di]
			d.State = core.DeviceConnected
			d.ActiveUUID = uuid
			d.ActiveName = p.Name
			d.IPv4 = ac.IPv4
			d.Gateway4 = gw
			d.DNS = ac.DNS
		}
		// Only one primary: any other layer-2 connection loses the default route.
		for i := range n.active {
			n.active[i].Default4 = false
			n.active[i].Default6 = false
		}
	} else {
		ac.VPNState = "activated"
	}
	n.active = append(n.active, ac)
	n.profiles[n.profileIndexLocked(uuid)].Timestamp = time.Now()
	n.emitLocked(core.Change{Kind: core.ChangeActive, Path: ac.Path})
	n.emitLocked(core.Change{Kind: core.ChangeDevices, Path: device})
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
}

func (n *NM) Deactivate(ctx context.Context, profileUUID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Deactivate"); err != nil {
		return err
	}
	if n.profileIndexLocked(profileUUID) < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: profile %s not found", profileUUID)
	}
	if !n.isActiveLocked(profileUUID) {
		return core.Errorf(core.KindConflict, "", "nm: profile %s is not active", profileUUID)
	}
	n.removeActiveLocked(func(a core.ActiveConnection) bool { return a.ProfileUUID == profileUUID })
	n.emitLocked(core.Change{Kind: core.ChangeActive, Path: profileUUID})
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
	return nil
}

func (n *NM) DisconnectDevice(ctx context.Context, device string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("DisconnectDevice"); err != nil {
		return err
	}
	if n.deviceIndexLocked(device) < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: device %q not found", device)
	}
	n.removeActiveLocked(func(a core.ActiveConnection) bool { return contains(a.Devices, device) })
	n.emitLocked(core.Change{Kind: core.ChangeActive, Path: device})
	n.emitLocked(core.Change{Kind: core.ChangeDevices, Path: device})
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
	return nil
}

func (n *NM) Forget(ctx context.Context, profileUUID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Forget"); err != nil {
		return err
	}
	i := n.profileIndexLocked(profileUUID)
	if i < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: profile %s not found", profileUUID)
	}
	n.removeActiveLocked(func(a core.ActiveConnection) bool { return a.ProfileUUID == profileUUID })
	n.profiles = append(n.profiles[:i], n.profiles[i+1:]...)
	for dev := range n.aps {
		for j := range n.aps[dev] {
			if n.aps[dev][j].ProfileUUID == profileUUID {
				n.aps[dev][j].Known = false
				n.aps[dev][j].ProfileUUID = ""
			}
		}
	}
	n.emitLocked(core.Change{Kind: core.ChangeProfiles, Path: profileUUID})
	n.emitLocked(core.Change{Kind: core.ChangeActive, Path: profileUUID})
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
	return nil
}

func (n *NM) SetWifiEnabled(ctx context.Context, on bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("SetWifiEnabled"); err != nil {
		return err
	}
	n.wifiEnabled = on
	if !on {
		n.removeActiveLocked(func(a core.ActiveConnection) bool { return a.Type == core.ProfileWifi })
		n.emitLocked(core.Change{Kind: core.ChangeActive})
	}
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
	return nil
}

func (n *NM) SetAutoconnect(ctx context.Context, profileUUID string, on bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("SetAutoconnect"); err != nil {
		return err
	}
	i := n.profileIndexLocked(profileUUID)
	if i < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: profile %s not found", profileUUID)
	}
	n.profiles[i].Autoconnect = on
	n.profiles[i].VersionID++
	n.emitLocked(core.Change{Kind: core.ChangeProfiles, Path: profileUUID})
	return nil
}

func (n *NM) UpdateIPConfig(ctx context.Context, profileUUID string, ipv4, ipv6 *core.IPConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("UpdateIPConfig"); err != nil {
		return err
	}
	i := n.profileIndexLocked(profileUUID)
	if i < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: profile %s not found", profileUUID)
	}
	if ipv4 != nil {
		n.profiles[i].IPv4 = *ipv4
	}
	if ipv6 != nil {
		n.profiles[i].IPv6 = *ipv6
	}
	n.profiles[i].VersionID++
	n.emitLocked(core.Change{Kind: core.ChangeProfiles, Path: profileUUID})
	return nil
}

func (n *NM) AddWireGuard(ctx context.Context, spec core.WireGuardSpec) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("AddWireGuard"); err != nil {
		return "", err
	}
	if spec.Name == "" || spec.PrivateKey == "" {
		return "", core.Errorf(core.KindInvalid, "", "nm: wireguard spec needs a name and private key")
	}
	uuid := n.newUUIDLocked()
	n.profiles = append(n.profiles, core.Profile{
		UUID: uuid, Name: spec.Name, Type: core.ProfileWireGuard, RawType: "wireguard", InterfaceName: spec.InterfaceName,
		Autoconnect: spec.Autoconnect, IPv4: core.IPConfig{Method: core.IPManual, Addresses: spec.Addresses, DNS: spec.DNS},
		IPv6: core.IPConfig{Method: core.IPDisabled}, VersionID: 1, Permissions: []string{"user:fake"},
	})
	n.emitLocked(core.Change{Kind: core.ChangeProfiles, Path: uuid})
	return uuid, nil
}

func (n *NM) ImportVPN(ctx context.Context, serviceKind, path string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("ImportVPN"); err != nil {
		return "", err
	}
	if path == "" {
		return "", core.Errorf(core.KindInvalid, "", "nm: import path is required")
	}
	name := path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, ".ovpn")
	name = strings.TrimSuffix(name, ".conf")
	uuid := n.newUUIDLocked()
	n.profiles = append(n.profiles, core.Profile{
		UUID: uuid, Name: name, Type: core.ProfileVPN, RawType: "vpn", VPNServiceType: "org.freedesktop.NetworkManager." + serviceKind,
		Autoconnect: false, IPv4: core.IPConfig{Method: core.IPAuto}, IPv6: core.IPConfig{Method: core.IPAuto}, VersionID: 1,
		Permissions: []string{"user:fake"},
	})
	n.emitLocked(core.Change{Kind: core.ChangeProfiles, Path: uuid})
	return uuid, nil
}

func (n *NM) SetProfilePermissions(ctx context.Context, profileUUID string, userOnly bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("SetProfilePermissions"); err != nil {
		return err
	}
	i := n.profileIndexLocked(profileUUID)
	if i < 0 {
		return core.Errorf(core.KindNotFound, "", "nm: profile %s not found", profileUUID)
	}
	if userOnly {
		n.profiles[i].Permissions = []string{"user:fake"}
	} else {
		n.profiles[i].Permissions = nil
	}
	n.profiles[i].VersionID++
	n.emitLocked(core.Change{Kind: core.ChangeProfiles, Path: profileUUID})
	return nil
}

// Watch returns a buffered channel of changes; it closes when ctx ends.
func (n *NM) Watch(ctx context.Context) (<-chan core.Change, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Watch"); err != nil {
		return nil, err
	}
	ch := make(chan core.Change, 64)
	n.watchers = append(n.watchers, ch)
	go func() {
		<-ctx.Done()
		n.mu.Lock()
		defer n.mu.Unlock()
		for i, w := range n.watchers {
			if w == ch {
				n.watchers = append(n.watchers[:i], n.watchers[i+1:]...)
				break
			}
		}
		close(ch)
	}()
	return ch, nil
}

func (n *NM) Permissions(ctx context.Context) (map[string]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.failing("Permissions"); err != nil {
		return nil, err
	}
	return copyMap(n.perms), nil
}

// --- mutators for tests ------------------------------------------------------

// SetConnectivity changes NM's connectivity verdict and emits a status change.
func (n *NM) SetConnectivity(c core.Connectivity) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.connect = c
	n.emitLocked(core.Change{Kind: core.ChangeStatus, Path: "connectivity"})
}

// DisconnectAll drops every active connection (a cable pulled, an AP gone).
func (n *NM) DisconnectAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.removeActiveLocked(func(core.ActiveConnection) bool { return true })
	n.emitLocked(core.Change{Kind: core.ChangeActive})
	n.emitLocked(core.Change{Kind: core.ChangeDevices})
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
}

// AddAP makes an SSID visible on a device (creating the device entry if needed).
func (n *NM) AddAP(device string, w core.WifiNetwork) {
	n.mu.Lock()
	defer n.mu.Unlock()
	w.Device = device
	for i := range n.aps[device] {
		if n.aps[device][i].SSID == w.SSID {
			n.aps[device][i] = w
			n.emitLocked(core.Change{Kind: core.ChangeWifi, Path: device})
			return
		}
	}
	n.aps[device] = append(n.aps[device], w)
	n.emitLocked(core.Change{Kind: core.ChangeWifi, Path: device})
}

// RemoveAP hides an SSID.
func (n *NM) RemoveAP(device, ssid string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	nets := n.aps[device]
	for i := range nets {
		if nets[i].SSID == ssid {
			n.aps[device] = append(nets[:i], nets[i+1:]...)
			break
		}
	}
	n.emitLocked(core.Change{Kind: core.ChangeWifi, Path: device})
}

// SetPrimaryState changes the primary Active Connection's state (activating,
// activated...) without removing it, to simulate a connection in progress.
func (n *NM) SetPrimaryState(s core.ActiveState) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p := n.primaryLocked(); p != nil {
		p.State = s
		n.emitLocked(core.Change{Kind: core.ChangeActive, Path: p.Path})
		n.emitLocked(core.Change{Kind: core.ChangeStatus})
	}
}

// Roam re-creates the primary Active Connection with a new NM path but the same
// profile, which is what NM does when the client reconnects to the same SSID.
func (n *NM) Roam() {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.primaryLocked()
	if p == nil {
		return
	}
	uuid, dev := p.ProfileUUID, ""
	if len(p.Devices) > 0 {
		dev = p.Devices[0]
	}
	n.removeActiveLocked(func(a core.ActiveConnection) bool { return a.ProfileUUID == uuid })
	n.emitLocked(core.Change{Kind: core.ChangeActive})
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
	n.activateLocked(uuid, dev)
}

// SetNetworking toggles NM's networking flag and, when off, drops every connection.
func (n *NM) SetNetworking(on bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.networking = on
	if !on {
		n.removeActiveLocked(func(core.ActiveConnection) bool { return true })
		n.emitLocked(core.Change{Kind: core.ChangeActive})
	}
	n.emitLocked(core.Change{Kind: core.ChangeStatus})
}

// SetPermission overrides one polkit action's answer.
func (n *NM) SetPermission(action, value string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.perms[action] = value
	n.emitLocked(core.Change{Kind: core.ChangeStatus, Path: "permissions"})
}

// Emit pushes an arbitrary change to watchers.
func (n *NM) Emit(c core.Change) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.emitLocked(c)
}

// --- helpers -------------------------------------------------------------------

func (n *NM) emitLocked(c core.Change) {
	for _, w := range n.watchers {
		select {
		case w <- c:
		default: // drop on full, never block a producer
		}
	}
}

func (n *NM) removeActiveLocked(match func(core.ActiveConnection) bool) {
	kept := n.active[:0]
	for _, a := range n.active {
		if !match(a) {
			kept = append(kept, a)
			continue
		}
		for _, dev := range a.Devices {
			if di := n.deviceIndexLocked(dev); di >= 0 {
				d := &n.devices[di]
				d.State = core.DeviceDisconnected
				d.ActiveUUID, d.ActiveName = "", ""
				d.IPv4, d.IPv6, d.DNS = nil, nil, nil
				d.Gateway4 = ""
			}
		}
	}
	n.active = kept
	n.reelectPrimaryLocked()
}

// reelectPrimaryLocked gives the default route to the first remaining layer-2
// connection when the previous primary went away, as NM does.
func (n *NM) reelectPrimaryLocked() {
	if n.primaryLocked() != nil {
		return
	}
	for i := range n.active {
		a := &n.active[i]
		if a.VPN || a.Type == core.ProfileVPN || a.Type == core.ProfileWireGuard || a.State != core.ActiveActivated {
			continue
		}
		a.Default4 = true
		return
	}
}

func (n *NM) isActiveLocked(uuid string) bool {
	for _, a := range n.active {
		if a.ProfileUUID == uuid {
			return true
		}
	}
	return false
}

func (n *NM) profileIndexLocked(uuid string) int {
	for i, p := range n.profiles {
		if p.UUID == uuid {
			return i
		}
	}
	return -1
}

func (n *NM) deviceIndexLocked(name string) int {
	for i, d := range n.devices {
		if d.Name == name {
			return i
		}
	}
	return -1
}

func (n *NM) newUUIDLocked() string {
	n.nextUUID++
	return fmt.Sprintf("%08x-0000-4000-8000-%012d", n.nextUUID, n.nextUUID)
}

// fakeAddress derives a stable address/gateway pair from a UUID.
func fakeAddress(uuid string) (ip, gw string) {
	if uuid == HomeUUID {
		return "192.168.1.42/24", "192.168.1.1" // matches the seed
	}
	sum := 0
	for _, c := range uuid {
		sum += int(c)
	}
	subnet := 10 + sum%200
	return fmt.Sprintf("192.168.%d.%d/24", subnet, 20+sum%100), fmt.Sprintf("192.168.%d.1", subnet)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
