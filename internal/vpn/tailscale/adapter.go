package tailscale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/user"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// VPNID is the stable core.VPN.ID of the single Tailscale entry.
const VPNID = "tailscale"

// Default control server; anything else is shown as a custom control URL (Headscale).
const defaultControlURL = "https://controlplane.tailscale.com"

// Adapter maps tailscaled onto core.VPNAdapter and core.TailscaleControl.
type Adapter struct {
	c   *Client
	log *slog.Logger

	// ProbeTTL bounds how long a Writable verdict is trusted before re-probing.
	ProbeTTL time.Duration
	// LoginTimeout bounds how long Login waits for tailscaled to produce a URL.
	LoginTimeout time.Duration
	// Backoff bounds for Watch reconnects.
	MinBackoff, MaxBackoff time.Duration

	mu         sync.Mutex
	writable   bool
	writableAt time.Time
	username   string
}

var (
	_ core.VPNAdapter       = (*Adapter)(nil)
	_ core.TailscaleControl = (*Adapter)(nil)
)

// NewAdapter wraps c. log may be nil (slog.Default()).
func NewAdapter(c *Client, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{
		c:            c,
		log:          log.With("pkg", "vpn/tailscale"),
		ProbeTTL:     time.Minute,
		LoginTimeout: 15 * time.Second,
		MinBackoff:   time.Second,
		MaxBackoff:   30 * time.Second,
		username:     currentUsername(),
	}
}

// Client exposes the raw LocalAPI client for callers that need more than the ports.
func (a *Adapter) Client() *Client { return a.c }

// Backend implements core.VPNAdapter.
func (a *Adapter) Backend() core.VPNBackend { return core.BackendTailscale }

// OperatorHint is the one-time fix for a read-only tailscaled, for the current user.
func (a *Adapter) OperatorHint() string {
	u := a.username
	if u == "" {
		u = "$USER"
	}
	return fmt.Sprintf("Tailscale is read-only for this user; run once: sudo tailscale set --operator=%s"+
		"  (NixOS: services.tailscale.extraSetFlags = [ \"--operator=%s\" ])", u, u)
}

func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

// MapState converts an ipn.State name to the common VPN state machine.
func MapState(backendState string) core.VPNState {
	switch backendState {
	case StateRunning:
		return core.VPNConnected
	case StateStarting, StateNoState:
		return core.VPNConnecting
	case StateStopped:
		return core.VPNDisconnected
	case StateNeedsLogin, StateNeedsMachineAuth:
		return core.VPNNeedsAuth
	case StateInUseOtherUser:
		return core.VPNNeedsSetup
	}
	return core.VPNError
}

// List implements core.VPNAdapter: exactly one VPN, even when tailscaled is down.
func (a *Adapter) List(ctx context.Context) ([]core.VPN, error) {
	v := core.VPN{ID: VPNID, Name: "Tailscale", Backend: core.BackendTailscale, Kind: "Tailscale"}
	st, err := a.c.Status(ctx)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			v.State = core.VPNUnavailable
			v.Detail = "tailscaled is not running (no socket at " + a.c.Socket() + ")"
			v.Error = err.Error()
			return []core.VPN{v}, nil
		}
		return nil, fmt.Errorf("tailscale: list: %w", err)
	}
	prefs, perr := a.c.Prefs(ctx)
	if perr != nil {
		a.log.Warn("prefs unavailable", "err", perr)
		prefs = &Prefs{}
	}
	v.State = MapState(st.BackendState)
	if h := customControlHost(prefs.ControlURL); h != "" {
		v.Name = "Tailscale (" + h + ")"
	}
	v.Writable = a.Writable(ctx)
	if !v.Writable {
		v.Detail = a.OperatorHint()
	}
	switch st.BackendState {
	case StateNeedsLogin, StateNeedsMachineAuth:
		v.AuthURL = st.AuthURL
		if st.BackendState == StateNeedsMachineAuth {
			v.Detail = "this machine must be approved by a tailnet admin"
		} else if v.AuthURL == "" && v.Writable {
			v.Detail = "not logged in; start a login to get a URL"
		}
	case StateInUseOtherUser:
		v.Detail = "tailscaled is in use by another user on this machine"
	}
	if v.State == core.VPNError {
		v.Error = "unknown backend state " + st.BackendState
	}
	v.Tailscale = buildInfo(st, prefs, v.Writable)
	return []core.VPN{v}, nil
}

// customControlHost returns the host of a non-default control server
// (Headscale and friends), or "" for the Tailscale SaaS control plane.
func customControlHost(controlURL string) string {
	switch controlURL {
	case "", defaultControlURL, "https://login.tailscale.com":
		return ""
	}
	u, err := url.Parse(controlURL)
	if err != nil || u.Host == "" {
		return controlURL
	}
	return u.Host
}

// buildInfo fills core.TailscaleInfo from a status and prefs snapshot.
func buildInfo(st *Status, prefs *Prefs, writable bool) *core.TailscaleInfo {
	suffix := st.MagicDNSSuffix
	if st.CurrentTailnet != nil && st.CurrentTailnet.MagicDNSSuffix != "" {
		suffix = st.CurrentTailnet.MagicDNSSuffix
	}
	info := &core.TailscaleInfo{
		BackendState: st.BackendState,
		Version:      st.Version,
		ControlURL:   prefs.ControlURL,
		MagicDNS:     suffix,
		SelfIPs:      st.TailscaleIPs,
		OperatorOK:   writable,
		ExitNodeID:   prefs.ExitNodeID,
		ExitNodeOn:   st.ExitNodeStatus != nil,
		AllowLAN:     prefs.ExitNodeAllowLANAccess,
		AcceptDNS:    prefs.CorpDNS,
		Health:       st.Health,
	}
	if st.CurrentTailnet != nil {
		info.Tailnet = st.CurrentTailnet.Name
	}
	if st.Self != nil {
		info.SelfName = peerName(st.Self, suffix)
	}
	if st.ExitNodeStatus != nil && st.ExitNodeStatus.ID != "" {
		info.ExitNodeID = st.ExitNodeStatus.ID
	}
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		cp := core.TailscalePeer{
			ID:             p.ID,
			Name:           peerName(p, suffix),
			HostName:       p.HostName,
			OS:             p.OS,
			IPs:            p.TailscaleIPs,
			Online:         p.Online,
			ExitNode:       p.ExitNode,
			ExitNodeOption: p.ExitNodeOption,
			Relay:          p.Relay,
		}
		if p.LastSeen.Year() > 1 {
			cp.LastSeen = p.LastSeen
		}
		if info.ExitNodeID != "" && p.ID == info.ExitNodeID {
			info.ExitNodeName = cp.Name
		}
		info.Peers = append(info.Peers, cp)
	}
	sort.Slice(info.Peers, func(i, j int) bool {
		if info.Peers[i].Name != info.Peers[j].Name {
			return info.Peers[i].Name < info.Peers[j].Name
		}
		return info.Peers[i].ID < info.Peers[j].ID
	})
	return info
}

// peerName is the MagicDNS base name: DNSName minus the tailnet suffix and dots.
func peerName(p *PeerStatus, suffix string) string {
	n := strings.TrimSuffix(p.DNSName, ".")
	if suffix != "" {
		n = strings.TrimSuffix(n, "."+strings.TrimSuffix(suffix, "."))
	}
	if n == "" {
		n = p.HostName
	}
	return n
}

// Writable reports whether this uid may change tailscaled prefs. It probes
// with an empty-mask PATCH (a no-op edit) and caches the verdict for ProbeTTL;
// any real write refreshes the cache too.
func (a *Adapter) Writable(ctx context.Context) bool {
	a.mu.Lock()
	if !a.writableAt.IsZero() && time.Since(a.writableAt) < a.ProbeTTL {
		w := a.writable
		a.mu.Unlock()
		return w
	}
	a.mu.Unlock()
	_, err := a.c.EditPrefs(ctx, MaskedPrefs{})
	switch {
	case err == nil:
		a.setWritable(true)
		return true
	case errors.Is(err, ErrAccessDenied):
		a.setWritable(false)
		return false
	default:
		// Daemon down or transient: do not cache, report read-only for now.
		a.log.Debug("writable probe failed", "err", err)
		return false
	}
}

func (a *Adapter) setWritable(w bool) {
	a.mu.Lock()
	a.writable, a.writableAt = w, time.Now()
	a.mu.Unlock()
}

// write runs a mutating call and turns 403 into a hint-bearing error.
func (a *Adapter) write(op string, fn func() error) error {
	err := fn()
	switch {
	case err == nil:
		a.setWritable(true)
		return nil
	case errors.Is(err, ErrAccessDenied):
		a.setWritable(false)
		// The hint travels structurally (core.HintOf), not inside the message.
		return core.Wrap(core.KindPermission, a.OperatorHint(), fmt.Errorf("tailscale: %s: %w", op, err))
	default:
		return fmt.Errorf("tailscale: %s: %w", op, err)
	}
}

func checkID(id string) error {
	if id != VPNID {
		return core.Errorf(core.KindNotFound, "", "tailscale: unknown VPN id %q", id)
	}
	return nil
}

// Connect implements core.VPNAdapter: WantRunning=true, plus an interactive
// login when the node has no session (the URL then shows up in List/Watch).
func (a *Adapter) Connect(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	err := a.write("connect", func() error {
		_, err := a.c.EditPrefs(ctx, MaskedPrefs{WantRunning: true, WantRunningSet: true})
		return err
	})
	if err != nil {
		return err
	}
	st, err := a.c.StatusWithoutPeers(ctx)
	if err != nil {
		return fmt.Errorf("tailscale: connect: status: %w", err)
	}
	if st.BackendState == StateNeedsLogin && st.AuthURL == "" {
		return a.write("connect: login", func() error { return a.c.StartLoginInteractive(ctx) })
	}
	return nil
}

// Disconnect implements core.VPNAdapter: WantRunning=false (what `tailscale down` does).
func (a *Adapter) Disconnect(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	return a.write("disconnect", func() error {
		_, err := a.c.EditPrefs(ctx, MaskedPrefs{WantRunning: false, WantRunningSet: true})
		return err
	})
}

// SetExitNode implements core.TailscaleControl. peer may be a stable node ID,
// a MagicDNS base name, a full DNS name, a hostname or a Tailscale IP; "" clears.
func (a *Adapter) SetExitNode(ctx context.Context, peer string, allowLAN bool) error {
	mp := MaskedPrefs{
		ExitNodeIDSet: true, ExitNodeIPSet: true,
		ExitNodeAllowLANAccess: allowLAN, ExitNodeAllowLANAccessSet: true,
	}
	if peer != "" {
		st, err := a.c.Status(ctx)
		if err != nil {
			return fmt.Errorf("tailscale: set exit node: %w", err)
		}
		p, err := resolvePeer(st, peer)
		if err != nil {
			return err
		}
		if !p.ExitNodeOption {
			return core.Errorf(core.KindInvalid, "", "tailscale: set exit node: %q does not offer an exit node", peer)
		}
		mp.ExitNodeID = p.ID
	}
	return a.write("set exit node", func() error {
		_, err := a.c.EditPrefs(ctx, mp)
		return err
	})
}

// resolvePeer finds the peer named by ref in st.
func resolvePeer(st *Status, ref string) (*PeerStatus, error) {
	suffix := st.MagicDNSSuffix
	if st.CurrentTailnet != nil && st.CurrentTailnet.MagicDNSSuffix != "" {
		suffix = st.CurrentTailnet.MagicDNSSuffix
	}
	lref := strings.ToLower(strings.TrimSuffix(ref, "."))
	var byName []*PeerStatus
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		if p.ID == ref {
			return p, nil
		}
		for _, ip := range p.TailscaleIPs {
			if ip == ref {
				return p, nil
			}
		}
		if strings.EqualFold(strings.TrimSuffix(p.DNSName, "."), lref) ||
			strings.EqualFold(peerName(p, suffix), lref) ||
			strings.EqualFold(p.HostName, lref) {
			byName = append(byName, p)
		}
	}
	switch len(byName) {
	case 0:
		return nil, core.Errorf(core.KindNotFound, "run `bnm vpn` to list peers", "tailscale: no peer matches %q", ref)
	case 1:
		return byName[0], nil
	}
	return nil, core.Errorf(core.KindInvalid, "use its ID or IP", "tailscale: ambiguous peer %q (%d matches)", ref, len(byName))
}

// UseExitNode implements core.TailscaleControl.
func (a *Adapter) UseExitNode(ctx context.Context, on bool) error {
	return a.write("use exit node", func() error {
		_, err := a.c.SetUseExitNode(ctx, on)
		return err
	})
}

// SetAcceptDNS implements core.TailscaleControl (MagicDNS / CorpDNS).
func (a *Adapter) SetAcceptDNS(ctx context.Context, on bool) error {
	return a.write("set accept-dns", func() error {
		_, err := a.c.EditPrefs(ctx, MaskedPrefs{CorpDNS: on, CorpDNSSet: true})
		return err
	})
}

// Login implements core.TailscaleControl: starts an interactive login and
// polls Status until tailscaled publishes the URL to open.
func (a *Adapter) Login(ctx context.Context) (string, error) {
	if err := a.write("login", func() error { return a.c.StartLoginInteractive(ctx) }); err != nil {
		return "", err
	}
	deadline := time.Now().Add(a.LoginTimeout)
	tick := 200 * time.Millisecond
	for {
		st, err := a.c.StatusWithoutPeers(ctx)
		if err != nil {
			return "", fmt.Errorf("tailscale: login: %w", err)
		}
		if st.AuthURL != "" {
			return st.AuthURL, nil
		}
		if st.BackendState == StateRunning {
			return "", fmt.Errorf("tailscale: login: already logged in")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("tailscale: login: no auth URL after %s (state %s)", a.LoginTimeout, st.BackendState)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("tailscale: login: %w", ctx.Err())
		case <-time.After(tick):
		}
	}
}

// Logout implements core.TailscaleControl.
func (a *Adapter) Logout(ctx context.Context) error {
	return a.write("logout", func() error { return a.c.Logout(ctx) })
}

// Watch implements core.VPNAdapter: one Change per IPN bus notify that carries
// state, prefs, a login URL, health or an error, plus one whenever the stream
// (re)connects or drops, so consumers re-read after a daemon restart.
func (a *Adapter) Watch(ctx context.Context) (<-chan core.Change, error) {
	ch := make(chan core.Change, 16)
	go a.watchLoop(ctx, ch)
	return ch, nil
}

func (a *Adapter) watchLoop(ctx context.Context, ch chan<- core.Change) {
	defer close(ch)
	emit := func(path string) {
		select {
		case ch <- core.Change{Kind: core.ChangeVPN, Path: path}:
		default: // drop on full; consumers re-read anyway
		}
	}
	backoff := a.MinBackoff
	failing := false // true while the stream is down, so a long outage emits once
	for {
		got := false
		err := a.c.WatchIPNBus(ctx, DefaultWatchMask, func(n Notify) bool {
			if !got {
				got = true
				failing = false
				backoff = a.MinBackoff
			}
			if n.State != nil || n.Prefs != nil || n.BrowseToURL != nil || n.Health != nil || n.ErrMessage != nil {
				emit(VPNID)
			}
			return true
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if got || !errors.Is(err, ErrUnavailable) {
				a.log.Debug("ipn bus stream ended", "err", err)
			}
			// The daemon went away or refused us; tell consumers to re-read once.
			if !failing {
				failing = true
				emit(VPNID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > a.MaxBackoff {
			backoff = a.MaxBackoff
		}
	}
}
