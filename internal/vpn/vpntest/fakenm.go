// Package vpntest holds FakeNM, an in-memory core.NetworkManager for unit
// tests of the VPN adapters and anything else that needs NM without D-Bus.
// It keeps profiles, active connections and polkit permissions in maps,
// records every call, and exposes a Watch channel tests push to.
// It has no tests of its own; the wireguard, nmvpn and vpn packages exercise it.
package vpntest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/dopeCape/better-nm/internal/core"
)

// Call is one recorded NetworkManager call (reads included).
type Call struct {
	Method string
	Args   []string
}

// FakeNM implements core.NetworkManager in memory. Zero value is usable.
type FakeNM struct {
	mu       sync.Mutex
	profiles map[string]core.Profile
	active   []core.ActiveConnection
	perms    map[string]string
	calls    []Call
	nextUUID int
	// Err, when non-nil, is returned by every method (simulates a dead D-Bus).
	Err error
	// Fail maps a method name to an error returned by that method only.
	Fail map[string]error

	watchMu  sync.Mutex
	watchers []chan core.Change
}

var _ core.NetworkManager = (*FakeNM)(nil)

// New returns a FakeNM with network-control and settings.modify.own allowed.
func New() *FakeNM {
	f := &FakeNM{}
	f.SetPermission("org.freedesktop.NetworkManager.network-control", core.PermYes)
	f.SetPermission("org.freedesktop.NetworkManager.settings.modify.own", core.PermYes)
	return f
}

func (f *FakeNM) init() {
	if f.profiles == nil {
		f.profiles = map[string]core.Profile{}
	}
	if f.perms == nil {
		f.perms = map[string]string{}
	}
}

func (f *FakeNM) fail(method string, args ...string) error {
	f.calls = append(f.calls, Call{Method: method, Args: args})
	if f.Err != nil {
		return f.Err
	}
	if err, ok := f.Fail[method]; ok {
		return err
	}
	return nil
}

// --- test controls ---

// AddProfile stores p (keyed by UUID; a missing UUID gets one generated) and returns the UUID.
func (f *FakeNM) AddProfile(p core.Profile) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if p.UUID == "" {
		p.UUID = f.genUUID()
	}
	f.profiles[p.UUID] = p
	return p.UUID
}

// RemoveProfile deletes a profile and any active connection for it.
func (f *FakeNM) RemoveProfile(uuid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.profiles, uuid)
	f.dropActive(uuid)
}

// SetActive replaces or adds the active connection for ac.ProfileUUID.
func (f *FakeNM) SetActive(ac core.ActiveConnection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropActive(ac.ProfileUUID)
	if ac.Path == "" {
		ac.Path = "/org/freedesktop/NetworkManager/ActiveConnection/" + ac.ProfileUUID
	}
	if p, ok := f.profiles[ac.ProfileUUID]; ok {
		if ac.ProfileName == "" {
			ac.ProfileName = p.Name
		}
		if ac.Type == "" {
			ac.Type = p.Type
		}
		p.Active = ac.State == core.ActiveActivated || ac.State == core.ActiveActivating
		f.profiles[ac.ProfileUUID] = p
	}
	f.active = append(f.active, ac)
}

// ClearActive removes the active connection for uuid, if any.
func (f *FakeNM) ClearActive(uuid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropActive(uuid)
}

func (f *FakeNM) dropActive(uuid string) {
	out := f.active[:0]
	for _, ac := range f.active {
		if ac.ProfileUUID != uuid {
			out = append(out, ac)
		}
	}
	f.active = out
	if p, ok := f.profiles[uuid]; ok {
		p.Active = false
		f.profiles[uuid] = p
	}
}

// SetPermission sets one polkit action result (yes|auth|no).
func (f *FakeNM) SetPermission(action, result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.perms[action] = result
}

// Calls returns every call made so far, in order.
func (f *FakeNM) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// ResetCalls forgets recorded calls.
func (f *FakeNM) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// Push delivers a Change to every Watch subscriber (non-blocking, drop on full).
func (f *FakeNM) Push(c core.Change) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	for _, ch := range f.watchers {
		select {
		case ch <- c:
		default:
		}
	}
}

func (f *FakeNM) genUUID() string {
	f.nextUUID++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextUUID)
}

// --- core.NetworkManager ---

func (f *FakeNM) Status(ctx context.Context) (core.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Status"); err != nil {
		return core.Status{}, err
	}
	st := core.Status{NMState: "connected-global", Connectivity: core.ConnFull, Networking: true, NMVersion: "fake"}
	st.Permissions = f.permsCopy()
	for i := range f.active {
		if f.active[i].Default4 {
			ac := f.active[i]
			st.Primary = &ac
		}
	}
	return st, nil
}

func (f *FakeNM) Devices(ctx context.Context) ([]core.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil, f.fail("Devices")
}

func (f *FakeNM) WifiNetworks(ctx context.Context, device string) ([]core.WifiNetwork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil, f.fail("WifiNetworks", device)
}

func (f *FakeNM) Scan(ctx context.Context, device string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail("Scan", device)
}

func (f *FakeNM) Profiles(ctx context.Context) ([]core.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Profiles"); err != nil {
		return nil, err
	}
	out := make([]core.Profile, 0, len(f.profiles))
	for _, p := range f.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	return out, nil
}

func (f *FakeNM) Profile(ctx context.Context, uuid string) (core.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Profile", uuid); err != nil {
		return core.Profile{}, err
	}
	p, ok := f.profiles[uuid]
	if !ok {
		return core.Profile{}, fmt.Errorf("fakenm: profile %s: %w", uuid, ErrNotFound)
	}
	return p, nil
}

func (f *FakeNM) ActiveConnections(ctx context.Context) ([]core.ActiveConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ActiveConnections"); err != nil {
		return nil, err
	}
	return append([]core.ActiveConnection(nil), f.active...), nil
}

func (f *FakeNM) ConnectWifi(ctx context.Context, req core.ConnectWifiRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail("ConnectWifi", req.SSID)
}

// Activate marks the profile activated (state activated, VPN state "activated"
// for vpn profiles) and pushes an "active" change.
func (f *FakeNM) Activate(ctx context.Context, profileUUID, device string) error {
	f.mu.Lock()
	if err := f.fail("Activate", profileUUID, device); err != nil {
		f.mu.Unlock()
		return err
	}
	p, ok := f.profiles[profileUUID]
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("fakenm: activate %s: %w", profileUUID, ErrNotFound)
	}
	ac := core.ActiveConnection{
		Path:        "/org/freedesktop/NetworkManager/ActiveConnection/" + profileUUID,
		ProfileUUID: profileUUID, ProfileName: p.Name, Type: p.Type,
		State: core.ActiveActivated, VPN: p.Type == core.ProfileVPN,
	}
	if device != "" {
		ac.Devices = []string{device}
	} else if p.InterfaceName != "" {
		ac.Devices = []string{p.InterfaceName}
	}
	if ac.VPN {
		ac.VPNState = "activated"
	}
	f.dropActive(profileUUID)
	f.active = append(f.active, ac)
	p.Active = true
	f.profiles[profileUUID] = p
	f.mu.Unlock()
	f.Push(core.Change{Kind: core.ChangeActive, Path: ac.Path})
	return nil
}

func (f *FakeNM) Deactivate(ctx context.Context, profileUUID string) error {
	f.mu.Lock()
	if err := f.fail("Deactivate", profileUUID); err != nil {
		f.mu.Unlock()
		return err
	}
	f.dropActive(profileUUID)
	f.mu.Unlock()
	f.Push(core.Change{Kind: core.ChangeActive, Path: profileUUID})
	return nil
}

func (f *FakeNM) DisconnectDevice(ctx context.Context, device string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail("DisconnectDevice", device)
}

func (f *FakeNM) Forget(ctx context.Context, profileUUID string) error {
	f.mu.Lock()
	if err := f.fail("Forget", profileUUID); err != nil {
		f.mu.Unlock()
		return err
	}
	delete(f.profiles, profileUUID)
	f.dropActive(profileUUID)
	f.mu.Unlock()
	f.Push(core.Change{Kind: core.ChangeProfiles, Path: profileUUID})
	return nil
}

func (f *FakeNM) SetWifiEnabled(ctx context.Context, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail("SetWifiEnabled", fmt.Sprint(on))
}

func (f *FakeNM) SetAutoconnect(ctx context.Context, profileUUID string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("SetAutoconnect", profileUUID, fmt.Sprint(on)); err != nil {
		return err
	}
	p, ok := f.profiles[profileUUID]
	if !ok {
		return fmt.Errorf("fakenm: %s: %w", profileUUID, ErrNotFound)
	}
	p.Autoconnect = on
	f.profiles[profileUUID] = p
	return nil
}

func (f *FakeNM) UpdateIPConfig(ctx context.Context, profileUUID string, ipv4, ipv6 *core.IPConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("UpdateIPConfig", profileUUID); err != nil {
		return err
	}
	p, ok := f.profiles[profileUUID]
	if !ok {
		return fmt.Errorf("fakenm: %s: %w", profileUUID, ErrNotFound)
	}
	if ipv4 != nil {
		p.IPv4 = *ipv4
	}
	if ipv6 != nil {
		p.IPv6 = *ipv6
	}
	f.profiles[profileUUID] = p
	return nil
}

// AddWireGuard stores a wireguard profile built from spec (private key dropped).
func (f *FakeNM) AddWireGuard(ctx context.Context, spec core.WireGuardSpec) (string, error) {
	f.mu.Lock()
	if err := f.fail("AddWireGuard", spec.Name); err != nil {
		f.mu.Unlock()
		return "", err
	}
	f.init()
	uuid := f.genUUID()
	p := core.Profile{
		UUID: uuid, Name: spec.Name, Type: core.ProfileWireGuard, RawType: "wireguard",
		InterfaceName: spec.InterfaceName, Autoconnect: spec.Autoconnect,
		Permissions: []string{"user:test:"},
		WireGuard: &core.WireGuardSetting{
			ListenPort: spec.ListenPort, FwMark: spec.FwMark, MTU: spec.MTU,
			Peers: append([]core.WireGuardPeer(nil), spec.Peers...),
		},
	}
	for _, a := range spec.Addresses {
		if isV6(a) {
			p.IPv6.Addresses = append(p.IPv6.Addresses, a)
		} else {
			p.IPv4.Addresses = append(p.IPv4.Addresses, a)
		}
	}
	for _, d := range spec.DNS {
		if isV6(d) {
			p.IPv6.DNS = append(p.IPv6.DNS, d)
		} else {
			p.IPv4.DNS = append(p.IPv4.DNS, d)
		}
	}
	p.IPv4.DNSSearch = spec.DNSSearch
	p.IPv4.Method = core.IPManual
	p.IPv6.Method = core.IPManual
	if len(p.IPv6.Addresses) == 0 {
		p.IPv6.Method = core.IPDisabled
	}
	f.profiles[uuid] = p
	f.mu.Unlock()
	f.Push(core.Change{Kind: core.ChangeProfiles, Path: uuid})
	return uuid, nil
}

// ImportVPN stores a vpn profile whose service type is derived from serviceKind
// ("openvpn" -> org.freedesktop.NetworkManager.openvpn) and whose name is path's base.
func (f *FakeNM) ImportVPN(ctx context.Context, serviceKind, path string) (string, error) {
	f.mu.Lock()
	if err := f.fail("ImportVPN", serviceKind, path); err != nil {
		f.mu.Unlock()
		return "", err
	}
	f.init()
	uuid := f.genUUID()
	name := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			name = path[i+1:]
			break
		}
	}
	f.profiles[uuid] = core.Profile{
		UUID: uuid, Name: name, Type: core.ProfileVPN, RawType: "vpn",
		VPNServiceType: "org.freedesktop.NetworkManager." + serviceKind,
	}
	f.mu.Unlock()
	f.Push(core.Change{Kind: core.ChangeProfiles, Path: uuid})
	return uuid, nil
}

func (f *FakeNM) SetProfilePermissions(ctx context.Context, profileUUID string, userOnly bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("SetProfilePermissions", profileUUID, fmt.Sprint(userOnly)); err != nil {
		return err
	}
	p, ok := f.profiles[profileUUID]
	if !ok {
		return fmt.Errorf("fakenm: %s: %w", profileUUID, ErrNotFound)
	}
	if userOnly {
		p.Permissions = []string{"user:test:"}
	} else {
		p.Permissions = nil
	}
	f.profiles[profileUUID] = p
	return nil
}

// Watch returns a buffered channel fed by Push; it closes when ctx ends.
func (f *FakeNM) Watch(ctx context.Context) (<-chan core.Change, error) {
	f.mu.Lock()
	err := f.fail("Watch")
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan core.Change, 32)
	f.watchMu.Lock()
	f.watchers = append(f.watchers, ch)
	f.watchMu.Unlock()
	go func() {
		<-ctx.Done()
		f.watchMu.Lock()
		for i, w := range f.watchers {
			if w == ch {
				f.watchers = append(f.watchers[:i], f.watchers[i+1:]...)
				break
			}
		}
		f.watchMu.Unlock()
		close(ch)
	}()
	return ch, nil
}

func (f *FakeNM) Permissions(ctx context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Permissions"); err != nil {
		return nil, err
	}
	return f.permsCopy(), nil
}

func (f *FakeNM) permsCopy() map[string]string {
	out := make(map[string]string, len(f.perms))
	for k, v := range f.perms {
		out[k] = v
	}
	return out
}

// ErrNotFound is returned for unknown profile UUIDs.
var ErrNotFound = errors.New("not found")

func isV6(cidr string) bool {
	for i := 0; i < len(cidr); i++ {
		if cidr[i] == ':' {
			return true
		}
		if cidr[i] == '/' {
			break
		}
	}
	return false
}
