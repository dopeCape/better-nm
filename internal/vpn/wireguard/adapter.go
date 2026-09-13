package wireguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/dopeCape/better-nm/internal/core"
)

// PermNetworkControl is the polkit action NM checks for Activate/Deactivate.
const PermNetworkControl = "org.freedesktop.NetworkManager.network-control"

// Adapter presents NM-native WireGuard profiles as VPNs.
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
	return &Adapter{nm: nm, log: log.With("pkg", "vpn/wireguard")}
}

// Backend implements core.VPNAdapter.
func (a *Adapter) Backend() core.VPNBackend { return core.BackendWireGuard }

// NetworkControlHint explains a non-"yes" network-control permission.
func NetworkControlHint(perm string) string {
	if perm == "" {
		perm = "unknown"
	}
	return fmt.Sprintf("NetworkManager will not let this session activate profiles (network-control: %s). "+
		"Log in to a local, active session, or grant %s via a polkit rule "+
		"(group networkmanager on NixOS, netdev on Debian/Ubuntu, wheel on Arch/Fedora).", perm, PermNetworkControl)
}

// List implements core.VPNAdapter.
func (a *Adapter) List(ctx context.Context) ([]core.VPN, error) {
	profiles, err := a.nm.Profiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("wireguard: list profiles: %w", err)
	}
	active, err := a.nm.ActiveConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("wireguard: list active: %w", err)
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
		if p.Type != core.ProfileWireGuard {
			continue
		}
		v := core.VPN{
			ID: p.UUID, Name: p.Name, Backend: core.BackendWireGuard, Kind: "WireGuard",
			State: core.VPNDisconnected, Writable: writable,
		}
		ac, isActive := byUUID[p.UUID]
		if isActive {
			switch ac.State {
			case core.ActiveActivating:
				v.State = core.VPNConnecting
			case core.ActiveActivated:
				v.State = core.VPNConnected
			}
		}
		if !writable {
			v.Detail = NetworkControlHint(perm)
			if v.State == core.VPNDisconnected {
				v.State = core.VPNNeedsSetup
			}
		}
		v.WireGuard = buildInfo(p, ac, isActive)
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

// buildInfo fills core.WireGuardInfo from the profile and, when active, the
// live connection. Kernel stats (handshake, bytes) stay zero: reading them
// needs netlink access bnm does not have.
func buildInfo(p core.Profile, ac core.ActiveConnection, isActive bool) *core.WireGuardInfo {
	info := &core.WireGuardInfo{InterfaceName: p.InterfaceName}
	if isActive && len(ac.Devices) > 0 && info.InterfaceName == "" {
		info.InterfaceName = ac.Devices[0]
	}
	if isActive && (len(ac.IPv4) > 0 || len(ac.IPv6) > 0) {
		info.Addresses = append(append([]string{}, ac.IPv4...), ac.IPv6...)
	} else {
		info.Addresses = append(append([]string{}, p.IPv4.Addresses...), p.IPv6.Addresses...)
	}
	if p.WireGuard != nil {
		info.PublicKey = p.WireGuard.PublicKey
		info.ListenPort = p.WireGuard.ListenPort
		info.Peers = append([]core.WireGuardPeer(nil), p.WireGuard.Peers...)
	}
	return info
}

func (a *Adapter) check(ctx context.Context, id string) error {
	p, err := a.nm.Profile(ctx, id)
	if err != nil {
		return fmt.Errorf("wireguard: profile %s: %w", id, err)
	}
	if p.Type != core.ProfileWireGuard {
		return fmt.Errorf("wireguard: profile %s is %s, not wireguard", id, p.Type)
	}
	return nil
}

// Connect implements core.VPNAdapter: NM activates the profile on its own device.
func (a *Adapter) Connect(ctx context.Context, id string) error {
	if err := a.check(ctx, id); err != nil {
		return err
	}
	if err := a.nm.Activate(ctx, id, ""); err != nil {
		return fmt.Errorf("wireguard: activate %s: %w", id, err)
	}
	return nil
}

// Disconnect implements core.VPNAdapter.
func (a *Adapter) Disconnect(ctx context.Context, id string) error {
	if err := a.check(ctx, id); err != nil {
		return err
	}
	if err := a.nm.Deactivate(ctx, id); err != nil {
		return fmt.Errorf("wireguard: deactivate %s: %w", id, err)
	}
	return nil
}

// Import parses a wg-quick config and creates the NM profile. The returned
// spec (with Unsupported filled) lets the caller warn about dropped keys.
func (a *Adapter) Import(ctx context.Context, name string, r io.Reader) (string, core.WireGuardSpec, error) {
	spec, err := ParseConf(name, r)
	if err != nil {
		return "", spec, err
	}
	uuid, err := a.nm.AddWireGuard(ctx, spec)
	if err != nil {
		return "", spec, fmt.Errorf("wireguard: add profile %q: %w", spec.Name, err)
	}
	if len(spec.Unsupported) > 0 {
		a.log.Info("imported with unsupported keys", "profile", spec.Name, "dropped", spec.Unsupported)
	}
	return uuid, spec, nil
}

// Watch implements core.VPNAdapter: NM "active" and "profiles" hints become
// ChangeVPN hints. Other NM hints are dropped.
func (a *Adapter) Watch(ctx context.Context) (<-chan core.Change, error) {
	src, err := a.nm.Watch(ctx)
	if err != nil {
		return nil, fmt.Errorf("wireguard: watch: %w", err)
	}
	return forward(ctx, src), nil
}

// forward relays active/profiles hints as ChangeVPN until src closes or ctx ends.
func forward(ctx context.Context, src <-chan core.Change) <-chan core.Change {
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
	return out
}

// IsInvalidConf reports whether err came from ParseConf.
func IsInvalidConf(err error) bool { return errors.Is(err, ErrInvalidConf) }
