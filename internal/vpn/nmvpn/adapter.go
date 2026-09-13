// Package nmvpn presents NetworkManager plugin VPN profiles (OpenVPN,
// strongSwan, Libreswan, L2TP, SSTP, PPTP, OpenConnect, Proton...) as VPNs.
// Every plugin rides the same path: a "vpn" profile keyed on vpn.service-type,
// activated and deactivated through core.NetworkManager, with NM's VpnState
// mapped onto the common state machine. Import delegates to nm.ImportVPN.
//
// Tested with vpntest.FakeNM; nothing here touches D-Bus.
package nmvpn

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dopeCape/better-nm/internal/core"
)

// PermNetworkControl is the polkit action NM checks for Activate/Deactivate.
const PermNetworkControl = "org.freedesktop.NetworkManager.network-control"

// ServiceTypePrefix is what every NM VPN plugin's service type starts with.
const ServiceTypePrefix = "org.freedesktop.NetworkManager."

// Adapter presents NM plugin VPN profiles as VPNs.
type Adapter struct {
	nm  core.NetworkManager
	log *slog.Logger
}

var _ core.VPNAdapter = (*Adapter)(nil)

// NewAdapter wraps nm. log may be nil (slog.Default()).
func NewAdapter(nm core.NetworkManager, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{nm: nm, log: log.With("pkg", "vpn/nmvpn")}
}

// Backend implements core.VPNAdapter.
func (a *Adapter) Backend() core.VPNBackend { return core.BackendNMVPN }

// NetworkControlHint explains a non-"yes" network-control permission.
func NetworkControlHint(perm string) string {
	if perm == "" {
		perm = "unknown"
	}
	return fmt.Sprintf("NetworkManager will not let this session activate profiles (network-control: %s). "+
		"Log in to a local, active session, or grant %s via a polkit rule "+
		"(group networkmanager on NixOS, netdev on Debian/Ubuntu, wheel on Arch/Fedora).", perm, PermNetworkControl)
}

// NormalizeVPNState turns any spelling of an NM VpnConnectionState into its
// canonical lower-case dashed name: "NM_VPN_CONNECTION_STATE_NEED_AUTH",
// "need_auth", "NeedAuth" and "need-auth" all become "need-auth".
func NormalizeVPNState(s string) string {
	s = strings.TrimPrefix(strings.ToUpper(s), "NM_VPN_CONNECTION_STATE_")
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "-")
	switch s {
	case "needauth":
		return "need-auth"
	case "ipconfigget", "ip-config", "ipconfig":
		return "ip-config-get"
	}
	return s
}

// MapState combines the ActiveConnection state and NM's VpnState. A vpn
// profile with no active connection is disconnected.
func MapState(ac core.ActiveConnection, isActive bool) (core.VPNState, string) {
	if !isActive {
		return core.VPNDisconnected, ""
	}
	vs := NormalizeVPNState(ac.VPNState)
	switch vs {
	case "need-auth":
		return core.VPNNeedsAuth, vs
	case "prepare", "connect", "ip-config-get":
		return core.VPNConnecting, vs
	case "activated":
		return core.VPNConnected, vs
	case "failed":
		return core.VPNError, vs
	case "disconnected":
		return core.VPNDisconnected, vs
	}
	// Unknown or empty VpnState: fall back to the ActiveConnection state.
	switch ac.State {
	case core.ActiveActivating:
		return core.VPNConnecting, vs
	case core.ActiveActivated:
		return core.VPNConnected, vs
	}
	return core.VPNDisconnected, vs
}

// List implements core.VPNAdapter.
func (a *Adapter) List(ctx context.Context) ([]core.VPN, error) {
	profiles, err := a.nm.Profiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("nmvpn: list profiles: %w", err)
	}
	active, err := a.nm.ActiveConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("nmvpn: list active: %w", err)
	}
	perm, perr := a.permission(ctx)
	if perr != nil {
		a.log.Warn("permissions unavailable", "err", perr)
	}
	writable := perm == core.PermYes

	byUUID := make(map[string]core.ActiveConnection, len(active))
	for _, ac := range active {
		byUUID[ac.ProfileUUID] = ac
	}
	var out []core.VPN
	for _, p := range profiles {
		if p.Type != core.ProfileVPN {
			continue
		}
		ac, isActive := byUUID[p.UUID]
		state, vs := MapState(ac, isActive)
		v := core.VPN{
			ID: p.UUID, Name: p.Name, Backend: core.BackendNMVPN,
			Kind: core.VPNKindForServiceType(p.VPNServiceType), State: state, Writable: writable,
		}
		if state == core.VPNError {
			v.Error = "VPN connection failed"
			if ac.VPNBanner != "" {
				v.Error += ": " + ac.VPNBanner
			}
		}
		if !writable {
			v.Detail = NetworkControlHint(perm)
			if v.State == core.VPNDisconnected {
				v.State = core.VPNNeedsSetup
			}
		}
		v.NMVPN = buildInfo(p, ac, vs)
		out = append(out, v)
	}
	return out, nil
}

func (a *Adapter) permission(ctx context.Context) (string, error) {
	perms, err := a.nm.Permissions(ctx)
	if err != nil {
		return "", err
	}
	if v, ok := perms[PermNetworkControl]; ok {
		return v, nil
	}
	if v, ok := perms["network-control"]; ok {
		return v, nil
	}
	return "", nil
}

func buildInfo(p core.Profile, ac core.ActiveConnection, vpnState string) *core.NMVPNInfo {
	info := &core.NMVPNInfo{ServiceType: p.VPNServiceType, Banner: ac.VPNBanner, NMVpnState: vpnState}
	if p.VPNData != nil {
		for _, k := range []string{"remote", "gateway", "gw", "server"} {
			if g := p.VPNData[k]; g != "" {
				info.Gateway = g
				break
			}
		}
		for _, k := range []string{"username", "user"} {
			if u := p.VPNData[k]; u != "" {
				info.Username = u
				break
			}
		}
	}
	return info
}

func (a *Adapter) check(ctx context.Context, id string) error {
	p, err := a.nm.Profile(ctx, id)
	if err != nil {
		return fmt.Errorf("nmvpn: profile %s: %w", id, err)
	}
	if p.Type != core.ProfileVPN {
		return fmt.Errorf("nmvpn: profile %s is %s, not a plugin VPN", id, p.Type)
	}
	return nil
}

// Connect implements core.VPNAdapter.
func (a *Adapter) Connect(ctx context.Context, id string) error {
	if err := a.check(ctx, id); err != nil {
		return err
	}
	if err := a.nm.Activate(ctx, id, ""); err != nil {
		return fmt.Errorf("nmvpn: activate %s: %w", id, err)
	}
	return nil
}

// Disconnect implements core.VPNAdapter.
func (a *Adapter) Disconnect(ctx context.Context, id string) error {
	if err := a.check(ctx, id); err != nil {
		return err
	}
	if err := a.nm.Deactivate(ctx, id); err != nil {
		return fmt.Errorf("nmvpn: deactivate %s: %w", id, err)
	}
	return nil
}

// Import creates a profile from a plugin file via nm.ImportVPN. kind is the
// nmcli import type ("openvpn", "libreswan", ...); a full service type is
// accepted and reduced to its last component.
func (a *Adapter) Import(ctx context.Context, kind, path string) (string, error) {
	kind = strings.TrimPrefix(kind, ServiceTypePrefix)
	if kind == "" {
		return "", fmt.Errorf("nmvpn: import: empty VPN kind")
	}
	uuid, err := a.nm.ImportVPN(ctx, kind, path)
	if err != nil {
		return "", fmt.Errorf("nmvpn: import %s %q: %w", kind, path, err)
	}
	return uuid, nil
}

// Watch implements core.VPNAdapter: NM "active" and "profiles" hints become
// ChangeVPN hints. Other NM hints are dropped.
func (a *Adapter) Watch(ctx context.Context) (<-chan core.Change, error) {
	src, err := a.nm.Watch(ctx)
	if err != nil {
		return nil, fmt.Errorf("nmvpn: watch: %w", err)
	}
	out := make(chan core.Change, 16)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-src:
				if !ok {
					return
				}
				if c.Kind != core.ChangeActive && c.Kind != core.ChangeProfiles {
					continue
				}
				select {
				case out <- core.Change{Kind: core.ChangeVPN, Path: c.Path}:
				default:
				}
			}
		}
	}()
	return out, nil
}
