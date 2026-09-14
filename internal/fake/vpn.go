package fake

import (
	"context"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// VPNAdapter is an in-memory core.VPNAdapter whose VPNs tests set directly.
// When Backend() is tailscale it also implements core.TailscaleControl.
type VPNAdapter struct {
	mu       sync.Mutex
	backend  core.VPNBackend
	vpns     []core.VPN
	fail     map[string]error
	watchers []chan core.Change
	// Latency delays Connect/Disconnect state flips (0 = instant).
	Latency time.Duration
	// LoginURL is what Login returns.
	LoginURL string
	// Secrets, when set on an nm-vpn adapter, makes Connect prompt for the VPN
	// password (once per Connect) the way the NM agent does.
	Secrets *SecretBroker
}

// NewVPNAdapter returns an adapter owning vpns.
func NewVPNAdapter(backend core.VPNBackend, vpns ...core.VPN) *VPNAdapter {
	for i := range vpns {
		if vpns[i].Backend == "" {
			vpns[i].Backend = backend
		}
		if vpns[i].State == "" {
			vpns[i].State = core.VPNDisconnected
		}
	}
	return &VPNAdapter{backend: backend, vpns: vpns, fail: map[string]error{}, LoginURL: "https://login.tailscale.com/a/fake"}
}

// NewTailscale returns a Tailscale adapter with one "tailscale" VPN and two peers.
func NewTailscale() *VPNAdapter {
	return NewVPNAdapter(core.BackendTailscale, core.VPN{
		ID: "tailscale", Name: "Tailscale", Kind: "Tailscale", State: core.VPNDisconnected, Writable: true,
		Tailscale: &core.TailscaleInfo{
			BackendState: "Stopped", Version: "1.80.0-fake", ControlURL: "https://controlplane.tailscale.com",
			Tailnet: "example.ts.net", MagicDNS: "example.ts.net", SelfIPs: []string{"100.64.0.1"}, SelfName: "laptop",
			OperatorOK: true, AcceptDNS: true,
			Peers: []core.TailscalePeer{
				{ID: "n1", Name: "homeserver", HostName: "homeserver", OS: "linux", IPs: []string{"100.64.0.2"}, Online: true, ExitNodeOption: true},
				{ID: "n2", Name: "phone", HostName: "phone", OS: "android", IPs: []string{"100.64.0.3"}, Online: false},
			},
		},
	})
}

// NewWireGuard returns a WireGuard adapter with one disconnected profile-backed VPN.
func NewWireGuard() *VPNAdapter {
	return NewVPNAdapter(core.BackendWireGuard, core.VPN{
		ID: "44444444-4444-4444-8444-444444444444", Name: "wg-home", Kind: "WireGuard", State: core.VPNDisconnected, Writable: true,
		WireGuard: &core.WireGuardInfo{InterfaceName: "wg0", ListenPort: 51820, Addresses: []string{"10.8.0.2/24"},
			Peers: []core.WireGuardPeer{{PublicKey: "peerpubkey=", Endpoint: "vpn.example.com:51820", AllowedIPs: []string{"0.0.0.0/0"}}}},
	})
}

// NewNMVPN returns an NM-plugin adapter with one OpenVPN profile.
func NewNMVPN() *VPNAdapter {
	return NewVPNAdapter(core.BackendNMVPN, core.VPN{
		ID: "55555555-5555-4555-8555-555555555555", Name: "office-ovpn", Kind: "OpenVPN", State: core.VPNDisconnected, Writable: true,
		NMVPN: &core.NMVPNInfo{ServiceType: "org.freedesktop.NetworkManager.openvpn", Gateway: "vpn.office.example"},
	})
}

// Fail makes method (Connect, Disconnect, List, SetExitNode, Login...) return err.
func (a *VPNAdapter) Fail(method string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		delete(a.fail, method)
		return
	}
	a.fail[method] = err
}

func (a *VPNAdapter) Backend() core.VPNBackend { return a.backend }

func (a *VPNAdapter) List(ctx context.Context) ([]core.VPN, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fail["List"]; err != nil {
		return nil, err
	}
	out := make([]core.VPN, len(a.vpns))
	for i, v := range a.vpns {
		out[i] = cloneVPN(v)
	}
	return out, nil
}

// cloneVPN deep-copies the per-backend info so a caller's snapshot never
// aliases state the fake mutates later (the race detector catches this otherwise).
func cloneVPN(v core.VPN) core.VPN {
	if v.Tailscale != nil {
		ts := *v.Tailscale
		ts.Peers = append([]core.TailscalePeer(nil), v.Tailscale.Peers...)
		ts.Health = append([]string(nil), v.Tailscale.Health...)
		v.Tailscale = &ts
	}
	if v.WireGuard != nil {
		wg := *v.WireGuard
		wg.Peers = append([]core.WireGuardPeer(nil), v.WireGuard.Peers...)
		v.WireGuard = &wg
	}
	if v.NMVPN != nil {
		n := *v.NMVPN
		v.NMVPN = &n
	}
	return v
}

func (a *VPNAdapter) Connect(ctx context.Context, id string) error {
	a.mu.Lock()
	b := a.Secrets
	failing := a.fail["Connect"] != nil
	var v *core.VPN
	if i := a.indexLocked(id); i >= 0 {
		vv := a.vpns[i]
		v = &vv
	}
	a.mu.Unlock()
	if b != nil && v != nil && a.backend == core.BackendNMVPN && !failing {
		req := core.SecretRequest{
			ConnectionUUID: v.ID, ConnectionName: v.Name, VPN: true, VPNKind: v.Kind, SettingName: "vpn",
			Fields:        []core.SecretField{{Key: "password", Label: "VPN password", Secret: true}},
			UserRequested: true,
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ans, ok := <-b.Raise(req):
			if !ok || ans.Secrets["password"] == "" {
				return core.Errorf(core.KindInvalid, "the password prompt was cancelled or not answered in time", "vpn: %s: no secrets available", v.Name)
			}
		}
	}
	return a.transition("Connect", id, core.VPNConnected)
}

func (a *VPNAdapter) Disconnect(ctx context.Context, id string) error {
	return a.transition("Disconnect", id, core.VPNDisconnected)
}

func (a *VPNAdapter) transition(method, id string, to core.VPNState) error {
	a.mu.Lock()
	if err := a.fail[method]; err != nil {
		a.mu.Unlock()
		return err
	}
	i := a.indexLocked(id)
	if i < 0 {
		a.mu.Unlock()
		return core.Errorf(core.KindNotFound, "", "vpn: %s not found", id)
	}
	if !a.vpns[i].Writable {
		a.mu.Unlock()
		return core.Errorf(core.KindPermission, a.vpns[i].Detail, "vpn: %s is not writable", id)
	}
	lat := a.Latency
	if lat == 0 {
		a.setStateLocked(i, to)
		a.mu.Unlock()
		return nil
	}
	a.setStateLocked(i, core.VPNConnecting)
	a.mu.Unlock()
	time.AfterFunc(lat, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if i := a.indexLocked(id); i >= 0 {
			a.setStateLocked(i, to)
		}
	})
	return nil
}

// SetState flips one VPN's state and notifies watchers.
func (a *VPNAdapter) SetState(id string, s core.VPNState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i := a.indexLocked(id); i >= 0 {
		a.setStateLocked(i, s)
	}
}

// Set replaces the VPN list wholesale.
func (a *VPNAdapter) Set(vpns ...core.VPN) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.vpns = vpns
	a.emitLocked(core.Change{Kind: core.ChangeVPN})
}

func (a *VPNAdapter) setStateLocked(i int, s core.VPNState) {
	v := &a.vpns[i]
	v.State = s
	v.Since = time.Now()
	if v.Tailscale != nil {
		switch s {
		case core.VPNConnected:
			v.Tailscale.BackendState = "Running"
		case core.VPNDisconnected:
			v.Tailscale.BackendState = "Stopped"
		case core.VPNNeedsAuth:
			v.Tailscale.BackendState = "NeedsLogin"
		}
	}
	a.emitLocked(core.Change{Kind: core.ChangeVPN, Path: v.ID})
}

func (a *VPNAdapter) Watch(ctx context.Context) (<-chan core.Change, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fail["Watch"]; err != nil {
		return nil, err
	}
	ch := make(chan core.Change, 64)
	a.watchers = append(a.watchers, ch)
	go func() {
		<-ctx.Done()
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, w := range a.watchers {
			if w == ch {
				a.watchers = append(a.watchers[:i], a.watchers[i+1:]...)
				break
			}
		}
		close(ch)
	}()
	return ch, nil
}

func (a *VPNAdapter) emitLocked(c core.Change) {
	for _, w := range a.watchers {
		select {
		case w <- c:
		default:
		}
	}
}

func (a *VPNAdapter) indexLocked(id string) int {
	for i, v := range a.vpns {
		if v.ID == id {
			return i
		}
	}
	return -1
}

// --- core.TailscaleControl ------------------------------------------------------

func (a *VPNAdapter) tsLocked() (*core.VPN, error) {
	if a.backend != core.BackendTailscale {
		return nil, core.Errorf(core.KindUnsupported, "", "vpn: not a tailscale adapter")
	}
	for i := range a.vpns {
		if a.vpns[i].Tailscale != nil {
			return &a.vpns[i], nil
		}
	}
	return nil, core.Errorf(core.KindUnavailable, "install tailscale and run `sudo tailscale up --operator=$USER`", "vpn: tailscaled is not running")
}

func (a *VPNAdapter) SetExitNode(ctx context.Context, peer string, allowLAN bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fail["SetExitNode"]; err != nil {
		return err
	}
	v, err := a.tsLocked()
	if err != nil {
		return err
	}
	ts := v.Tailscale
	if peer == "" {
		ts.ExitNodeID, ts.ExitNodeName, ts.ExitNodeOn = "", "", false
		for i := range ts.Peers {
			ts.Peers[i].ExitNode = false
		}
		a.emitLocked(core.Change{Kind: core.ChangeVPN, Path: v.ID})
		return nil
	}
	for i := range ts.Peers {
		p := &ts.Peers[i]
		if p.ID == peer || p.Name == peer {
			if !p.ExitNodeOption {
				return core.Errorf(core.KindInvalid, "", "vpn: peer %s does not offer an exit node", peer)
			}
			for j := range ts.Peers {
				ts.Peers[j].ExitNode = false
			}
			p.ExitNode = true
			ts.ExitNodeID, ts.ExitNodeName, ts.ExitNodeOn, ts.AllowLAN = p.ID, p.Name, true, allowLAN
			a.emitLocked(core.Change{Kind: core.ChangeVPN, Path: v.ID})
			return nil
		}
	}
	return core.Errorf(core.KindNotFound, "", "vpn: peer %s not found", peer)
}

func (a *VPNAdapter) UseExitNode(ctx context.Context, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fail["UseExitNode"]; err != nil {
		return err
	}
	v, err := a.tsLocked()
	if err != nil {
		return err
	}
	if on && v.Tailscale.ExitNodeID == "" {
		return core.Errorf(core.KindInvalid, "pick one with `bnm vpn exit-node <peer>`", "vpn: no exit node selected")
	}
	v.Tailscale.ExitNodeOn = on
	a.emitLocked(core.Change{Kind: core.ChangeVPN, Path: v.ID})
	return nil
}

func (a *VPNAdapter) SetAcceptDNS(ctx context.Context, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fail["SetAcceptDNS"]; err != nil {
		return err
	}
	v, err := a.tsLocked()
	if err != nil {
		return err
	}
	v.Tailscale.AcceptDNS = on
	a.emitLocked(core.Change{Kind: core.ChangeVPN, Path: v.ID})
	return nil
}

func (a *VPNAdapter) Login(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fail["Login"]; err != nil {
		return "", err
	}
	v, err := a.tsLocked()
	if err != nil {
		return "", err
	}
	v.State = core.VPNNeedsAuth
	v.AuthURL = a.LoginURL
	v.Tailscale.BackendState = "NeedsLogin"
	a.emitLocked(core.Change{Kind: core.ChangeVPN, Path: v.ID})
	return a.LoginURL, nil
}

func (a *VPNAdapter) Logout(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fail["Logout"]; err != nil {
		return err
	}
	v, err := a.tsLocked()
	if err != nil {
		return err
	}
	v.State = core.VPNNeedsAuth
	v.AuthURL = ""
	v.Tailscale.BackendState = "NeedsLogin"
	a.emitLocked(core.Change{Kind: core.ChangeVPN, Path: v.ID})
	return nil
}

// VPNRegistry fans a set of adapters into one list, mirroring internal/vpn.Registry.
type VPNRegistry struct {
	adapters []core.VPNAdapter
}

// NewVPNRegistry composes adapters.
func NewVPNRegistry(adapters ...core.VPNAdapter) *VPNRegistry {
	return &VPNRegistry{adapters: adapters}
}

func (r *VPNRegistry) List(ctx context.Context) ([]core.VPN, error) {
	var out []core.VPN
	for _, a := range r.adapters {
		vs, err := a.List(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	return out, nil
}

func (r *VPNRegistry) owner(ctx context.Context, id string) (core.VPNAdapter, error) {
	for _, a := range r.adapters {
		vs, err := a.List(ctx)
		if err != nil {
			continue
		}
		for _, v := range vs {
			if v.ID == id {
				return a, nil
			}
		}
	}
	return nil, core.Errorf(core.KindNotFound, "run `bnm vpn list`", "vpn: %s not found", id)
}

func (r *VPNRegistry) Connect(ctx context.Context, id string) error {
	a, err := r.owner(ctx, id)
	if err != nil {
		return err
	}
	return a.Connect(ctx, id)
}

func (r *VPNRegistry) Disconnect(ctx context.Context, id string) error {
	a, err := r.owner(ctx, id)
	if err != nil {
		return err
	}
	return a.Disconnect(ctx, id)
}

// Watch merges every adapter's Watch into one channel; closes when ctx ends.
func (r *VPNRegistry) Watch(ctx context.Context) (<-chan core.Change, error) {
	out := make(chan core.Change, 64)
	var wg sync.WaitGroup
	for _, a := range r.adapters {
		ch, err := a.Watch(ctx)
		if err != nil {
			return nil, err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range ch {
				select {
				case out <- c:
				default:
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out, nil
}

// Tailscale returns the adapter implementing core.TailscaleControl, or nil.
func (r *VPNRegistry) Tailscale() core.TailscaleControl {
	for _, a := range r.adapters {
		if a.Backend() != core.BackendTailscale {
			continue
		}
		if tc, ok := a.(core.TailscaleControl); ok {
			return tc
		}
	}
	return nil
}
