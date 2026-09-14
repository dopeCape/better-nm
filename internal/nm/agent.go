package nm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// The NetworkManager secret agent: bnmd exports
// org.freedesktop.NetworkManager.SecretAgent on its bus connection and
// registers it with AgentManager, so a secret NM does not have (a wrong
// Wi-Fi password being retried, an OTP, a VPN password the profile does not
// store) becomes a core.SecretRequest the surfaces can answer instead of the
// activation failing with "No agents were available".
//
// godbus runs every incoming method call on its own goroutine, so GetSecrets
// simply blocks until the request is answered, cancelled (by a surface or by
// NM's CancelGetSecrets) or expires; the reply carries the secrets, and the
// error names NM understands (NoSecrets, UserCanceled, AgentCanceled) tell it
// what to do next.
const (
	// AgentIdentifier is the identifier registered with AgentManager (3-255
	// chars of [A-Za-z0-9_-.], unique per user session).
	AgentIdentifier = "io.github.dopecape.bnm"
	// DefaultSecretTimeout is how long a request stays answerable; NM itself
	// gives up on an agent after about two minutes.
	DefaultSecretTimeout = 120 * time.Second

	pathAgent dbus.ObjectPath = "/org/freedesktop/NetworkManager/SecretAgent"

	ifaceAgentMgr       = "org.freedesktop.NetworkManager.AgentManager"
	ifaceSecretAgent    = "org.freedesktop.NetworkManager.SecretAgent"
	ifaceIntrospectable = "org.freedesktop.DBus.Introspectable"

	errAgentNoSecrets     = ifaceSecretAgent + ".Error.NoSecrets"
	errAgentUserCanceled  = ifaceSecretAgent + ".Error.UserCanceled"
	errAgentAgentCanceled = ifaceSecretAgent + ".Error.AgentCanceled"
	errAgentFailed        = ifaceSecretAgent + ".Error.Failed"

	// NMSecretAgentCapabilities.
	agentCapVPNHints uint32 = 0x1

	// NMSecretAgentGetSecretsFlags.
	getSecretsAllowInteraction uint32 = 0x1
	getSecretsRequestNew       uint32 = 0x2
	getSecretsUserRequested    uint32 = 0x4

	// vpnMessageHint prefixes free text a VPN plugin wants shown with the prompt.
	vpnMessageHint = "x-vpn-message:"

	// secretResolvedTTL is how long a resolved request stays known, so a late
	// second answer is told "already answered" (409) rather than "gone" (404).
	secretResolvedTTL = 30 * time.Second
	// secretPollInterval is how often an activation wait re-checks whether a
	// secret request for its profile is still pending.
	secretPollInterval = time.Second
)

// agent owns the SecretAgent export and the pending request map.
type agent struct {
	conn    *dbus.Conn // nil in pure unit tests (no export, no registration)
	log     *slog.Logger
	ctx     context.Context // client lifetime; ending it cancels every pending request
	timeout time.Duration
	now     func() time.Time
	// trusted says whether a GetSecrets caller is NetworkManager; nil trusts everyone.
	trusted func(sender string) bool
	// promptAutoconnect lets requests without USER_REQUESTED (autoconnect)
	// prompt too; off by default, see getSecrets.
	promptAutoconnect bool
	// persist is called (in its own goroutine) after an answer with Save=true
	// for secrets the profile marks agent-owned or not-saved, since NM only
	// stores what the profile calls system-owned. nil = no persistence.
	persist func(connPath dbus.ObjectPath, setting string, req core.SecretRequest, a core.SecretAnswer, flags map[string]uint32)

	mu         sync.Mutex
	pending    map[string]*secretRequest
	onNeeded   func(core.SecretRequest)
	onResolved func(id string, outcome core.SecretOutcome)
	registered bool
	exported   bool
}

// secretRequest is one outstanding GetSecrets call.
type secretRequest struct {
	req      core.SecretRequest
	connPath dbus.ObjectPath
	setting  string
	flags    map[string]uint32 // the profile's own secret flags per requested key
	done     chan struct{}     // closed on resolution
	answer   core.SecretAnswer
	outcome  core.SecretOutcome
	byNM     bool // cancelled by CancelGetSecrets (AgentCanceled) rather than a person (UserCanceled)
	resolved bool
}

func newAgent(conn *dbus.Conn, ctx context.Context, log *slog.Logger, timeout time.Duration) *agent {
	if timeout <= 0 {
		timeout = DefaultSecretTimeout
	}
	if log == nil {
		log = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &agent{
		conn:    conn,
		log:     log,
		ctx:     ctx,
		timeout: timeout,
		now:     time.Now,
		pending: map[string]*secretRequest{},
	}
}

// secretAgentObject is the value exported on the bus; only these four methods
// are visible.
type secretAgentObject struct{ a *agent }

// agentNode is the introspection data for the export.
var agentNode = &introspect.Node{
	Name: string(pathAgent),
	Interfaces: []introspect.Interface{
		introspect.IntrospectData,
		{
			Name: ifaceSecretAgent,
			Methods: []introspect.Method{
				{Name: "GetSecrets", Args: []introspect.Arg{
					{Name: "connection", Type: "a{sa{sv}}", Direction: "in"},
					{Name: "connection_path", Type: "o", Direction: "in"},
					{Name: "setting_name", Type: "s", Direction: "in"},
					{Name: "hints", Type: "as", Direction: "in"},
					{Name: "flags", Type: "u", Direction: "in"},
					{Name: "secrets", Type: "a{sa{sv}}", Direction: "out"},
				}},
				{Name: "CancelGetSecrets", Args: []introspect.Arg{
					{Name: "connection_path", Type: "o", Direction: "in"},
					{Name: "setting_name", Type: "s", Direction: "in"},
				}},
				{Name: "SaveSecrets", Args: []introspect.Arg{
					{Name: "connection", Type: "a{sa{sv}}", Direction: "in"},
					{Name: "connection_path", Type: "o", Direction: "in"},
				}},
				{Name: "DeleteSecrets", Args: []introspect.Arg{
					{Name: "connection", Type: "a{sa{sv}}", Direction: "in"},
					{Name: "connection_path", Type: "o", Direction: "in"},
				}},
			},
		},
	},
}

// export puts the SecretAgent object on the bus (idempotent).
func (a *agent) export() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.exported {
		return nil
	}
	if a.conn == nil {
		return errors.New("nm: agent: no bus connection")
	}
	if err := a.conn.Export(secretAgentObject{a}, pathAgent, ifaceSecretAgent); err != nil {
		return fmt.Errorf("nm: agent: export %s: %w", ifaceSecretAgent, err)
	}
	if err := a.conn.Export(introspect.NewIntrospectable(agentNode), pathAgent, ifaceIntrospectable); err != nil {
		return fmt.Errorf("nm: agent: export introspection: %w", err)
	}
	a.exported = true
	return nil
}

// register calls AgentManager.RegisterWithCapabilities. NM forgets agents when
// it restarts, so the client calls this again on NameOwnerChanged.
func (a *agent) register(ctx context.Context) error {
	if a.conn == nil {
		return errors.New("nm: agent: no bus connection")
	}
	if err := a.export(); err != nil {
		return err
	}
	err := a.conn.Object(busName, pathAgentMgr).CallWithContext(ctx, ifaceAgentMgr+".RegisterWithCapabilities", 0,
		AgentIdentifier, agentCapVPNHints).Err
	a.mu.Lock()
	a.registered = err == nil
	a.mu.Unlock()
	if err != nil {
		return wrapDBus("register secret agent", err)
	}
	return nil
}

// unregister tells AgentManager we are going away (best effort).
func (a *agent) unregister(ctx context.Context) {
	a.mu.Lock()
	reg := a.registered
	a.registered = false
	a.mu.Unlock()
	if !reg || a.conn == nil {
		return
	}
	if err := a.conn.Object(busName, pathAgentMgr).CallWithContext(ctx, ifaceAgentMgr+".Unregister", 0).Err; err != nil {
		a.log.Debug("nm: agent: unregister failed", "err", err)
	}
}

// markUnregistered records that NM left the bus (its agent list died with it).
func (a *agent) markUnregistered() {
	a.mu.Lock()
	a.registered = false
	a.mu.Unlock()
}

func (a *agent) requestTimeout() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.timeout
}

func (a *agent) setTimeout(d time.Duration) {
	a.mu.Lock()
	a.timeout = d
	a.mu.Unlock()
}

func (a *agent) isRegistered() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registered
}

func (a *agent) setNeeded(f func(core.SecretRequest)) {
	a.mu.Lock()
	a.onNeeded = f
	a.mu.Unlock()
}

func (a *agent) setResolved(f func(string, core.SecretOutcome)) {
	a.mu.Lock()
	a.onResolved = f
	a.mu.Unlock()
}

// ---- the exported methods ----

func (o secretAgentObject) GetSecrets(sender dbus.Sender, connection settingsDict, connPath dbus.ObjectPath, settingName string, hints []string, flags uint32) (settingsDict, *dbus.Error) {
	return o.a.getSecrets(string(sender), connection, connPath, settingName, hints, flags)
}

func (o secretAgentObject) CancelGetSecrets(connPath dbus.ObjectPath, settingName string) *dbus.Error {
	o.a.cancelByNM(connPath, settingName)
	return nil
}

// SaveSecrets is a no-op: bnm returns system-owned secrets, which NM persists
// itself, and never holds agent-owned ones.
func (o secretAgentObject) SaveSecrets(connection settingsDict, connPath dbus.ObjectPath) *dbus.Error {
	o.a.log.Debug("nm: agent: SaveSecrets ignored", "path", connPath, "id", vStr(connection[settingConnection], "id"))
	return nil
}

// DeleteSecrets is a no-op for the same reason.
func (o secretAgentObject) DeleteSecrets(connection settingsDict, connPath dbus.ObjectPath) *dbus.Error {
	o.a.log.Debug("nm: agent: DeleteSecrets ignored", "path", connPath, "id", vStr(connection[settingConnection], "id"))
	return nil
}

func (a *agent) getSecrets(sender string, connection settingsDict, connPath dbus.ObjectPath, settingName string, hints []string, flags uint32) (settingsDict, *dbus.Error) {
	if a.trusted != nil && !a.trusted(sender) {
		a.log.Warn("nm: agent: GetSecrets from a caller that is not NetworkManager; refused", "sender", sender, "path", connPath)
		return nil, dbus.NewError(errAgentFailed, []any{"caller is not NetworkManager"})
	}
	name := vStr(connection[settingConnection], "id")
	if flags&getSecretsAllowInteraction == 0 {
		// NM asks every agent silently first; we store nothing, so let it move on.
		a.log.Debug("nm: agent: GetSecrets without interaction: no secrets", "id", name, "setting", settingName)
		return nil, dbus.NewError(errAgentNoSecrets, []any{"bnm holds no secrets; it can only prompt"})
	}
	if flags&getSecretsUserRequested == 0 && !a.promptAutoconnect {
		// An autoconnect attempt, not something a person started. A prompt
		// nobody is looking at would hold the device in NEED_AUTH until it
		// expires and keep NM from falling back to another network (seen
		// live: a two-minute outage). Decline: NM marks the profile
		// autoconnect-blocked for want of secrets and moves on; an explicit
		// activation from any surface is user-requested and does prompt.
		a.log.Info("nm: agent: autoconnect needs a secret; declining without a prompt", "profile", name, "setting", settingName, "request_new", flags&getSecretsRequestNew != 0)
		return nil, dbus.NewError(errAgentNoSecrets, []any{"bnm does not prompt for autoconnect activations; connect explicitly to be asked"})
	}
	a.mu.Lock()
	needed := a.onNeeded
	a.mu.Unlock()
	if needed == nil {
		a.log.Warn("nm: agent: GetSecrets but no surface can prompt", "id", name, "setting", settingName)
		return nil, dbus.NewError(errAgentNoSecrets, []any{"no bnm surface is attached to prompt"})
	}
	id, err := randomID()
	if err != nil {
		return nil, dbus.NewError(errAgentFailed, []any{err.Error()})
	}
	now := a.now()
	timeout := a.requestTimeout()
	req := buildSecretRequest(id, connection, settingName, hints, flags, now, now.Add(timeout))
	p := &secretRequest{
		req:      req,
		connPath: connPath,
		setting:  settingName,
		flags:    secretFlagsOf(connection, settingName, req.Fields),
		done:     make(chan struct{}),
	}
	a.mu.Lock()
	// NM never has two requests open for one (profile, setting); a leftover
	// means it gave up on the previous one without telling us.
	for _, q := range a.pending {
		if !q.resolved && q.connPath == connPath && q.setting == settingName {
			a.resolveLocked(q, core.SecretCancelled, core.SecretAnswer{}, true)
		}
	}
	a.pending[id] = p
	a.mu.Unlock()
	a.log.Info("nm: agent: secret requested", "request", id, "profile", name, "setting", settingName,
		"hints", strings.Join(hints, ","), "request_new", req.RequestNew, "user_requested", req.UserRequested)
	needed(req)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		a.resolve(id, core.SecretTimeout, core.SecretAnswer{}, false)
	case <-a.ctx.Done():
		a.resolve(id, core.SecretCancelled, core.SecretAnswer{}, false)
	}
	<-p.done
	a.mu.Lock()
	outcome, answer, byNM := p.outcome, p.answer, p.byNM
	a.mu.Unlock()
	time.AfterFunc(secretResolvedTTL, func() { a.forget(id) })

	switch outcome {
	case core.SecretAnswered:
		a.log.Info("nm: agent: secret answered", "request", id, "profile", name, "save", answer.Save)
		if answer.Save && a.persist != nil && needsPersist(p.flags) {
			go a.persist(connPath, settingName, req, answer, p.flags)
		}
		return secretsReply(settingName, req, answer), nil
	case core.SecretTimeout:
		a.log.Info("nm: agent: secret request timed out", "request", id, "profile", name)
		return nil, dbus.NewError(errAgentUserCanceled, []any{"nobody answered the bnm password prompt within " + timeout.String()})
	default:
		if byNM {
			a.log.Info("nm: agent: secret request cancelled by NetworkManager", "request", id, "profile", name)
			return nil, dbus.NewError(errAgentAgentCanceled, []any{"request cancelled"})
		}
		a.log.Info("nm: agent: secret request cancelled", "request", id, "profile", name)
		return nil, dbus.NewError(errAgentUserCanceled, []any{"the bnm password prompt was cancelled"})
	}
}

// ---- resolution ----

// resolveLocked marks p done; caller holds a.mu. It returns the callback to
// run after unlocking (nil when p was already resolved).
func (a *agent) resolveLocked(p *secretRequest, outcome core.SecretOutcome, ans core.SecretAnswer, byNM bool) func() {
	if p.resolved {
		return nil
	}
	p.resolved = true
	p.outcome = outcome
	p.answer = ans
	p.byNM = byNM
	close(p.done)
	if a.onResolved == nil {
		return nil
	}
	f, id := a.onResolved, p.req.ID
	return func() { f(id, outcome) }
}

func (a *agent) resolve(id string, outcome core.SecretOutcome, ans core.SecretAnswer, byNM bool) bool {
	a.mu.Lock()
	p := a.pending[id]
	if p == nil {
		a.mu.Unlock()
		return false
	}
	cb := a.resolveLocked(p, outcome, ans, byNM)
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
	return true
}

func (a *agent) forget(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

// cancelByNM implements CancelGetSecrets.
func (a *agent) cancelByNM(connPath dbus.ObjectPath, settingName string) {
	var cbs []func()
	a.mu.Lock()
	for _, p := range a.pending {
		if !p.resolved && p.connPath == connPath && p.setting == settingName {
			if cb := a.resolveLocked(p, core.SecretCancelled, core.SecretAnswer{}, true); cb != nil {
				cbs = append(cbs, cb)
			}
		}
	}
	a.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

// cancelAll resolves every pending request (client shutdown).
func (a *agent) cancelAll() {
	var cbs []func()
	a.mu.Lock()
	for _, p := range a.pending {
		if cb := a.resolveLocked(p, core.SecretCancelled, core.SecretAnswer{}, false); cb != nil {
			cbs = append(cbs, cb)
		}
	}
	a.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

// pendingList is the broker's Pending: unresolved requests, oldest first.
func (a *agent) pendingList() []core.SecretRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.SecretRequest, 0, len(a.pending))
	for _, p := range a.pending {
		if !p.resolved {
			out = append(out, cloneRequest(p.req))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// answer is the broker's Answer.
func (a *agent) answer(id string, ans core.SecretAnswer) error {
	op := "answer secret request " + id
	a.mu.Lock()
	p := a.pending[id]
	if p == nil {
		a.mu.Unlock()
		return newErr(op, ErrNotFound, "no such request (it may have expired)")
	}
	if p.resolved {
		outcome := p.outcome
		a.mu.Unlock()
		return newErr(op, ErrConflict, "request already "+outcomeWord(outcome))
	}
	got := 0
	for _, f := range p.req.Fields {
		if _, ok := ans.Secrets[f.Key]; ok {
			got++
		}
	}
	if got == 0 {
		a.mu.Unlock()
		keys := make([]string, 0, len(p.req.Fields))
		for _, f := range p.req.Fields {
			keys = append(keys, f.Key)
		}
		return newErr(op, nil, "answer carries none of the requested secrets ("+strings.Join(keys, ", ")+")")
	}
	cb := a.resolveLocked(p, core.SecretAnswered, ans, false)
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
	return nil
}

// cancel is the broker's Cancel.
func (a *agent) cancel(id string) error {
	op := "cancel secret request " + id
	a.mu.Lock()
	p := a.pending[id]
	if p == nil {
		a.mu.Unlock()
		return newErr(op, ErrNotFound, "no such request (it may have expired)")
	}
	if p.resolved {
		outcome := p.outcome
		a.mu.Unlock()
		return newErr(op, ErrConflict, "request already "+outcomeWord(outcome))
	}
	cb := a.resolveLocked(p, core.SecretCancelled, core.SecretAnswer{}, false)
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
	return nil
}

// pendingFor says whether a request for the profile at connPath is open.
func (a *agent) pendingFor(connPath dbus.ObjectPath) bool {
	if !realPath(connPath) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.pending {
		if !p.resolved && p.connPath == connPath {
			return true
		}
	}
	return false
}

func outcomeWord(o core.SecretOutcome) string {
	switch o {
	case core.SecretAnswered:
		return "answered"
	case core.SecretTimeout:
		return "timed out"
	default:
		return "cancelled"
	}
}

// ---- request building ----

// buildSecretRequest turns GetSecrets' arguments into what a surface shows.
// The connection dict arrives without secrets but with everything else
// (uuid, id, ssid, vpn.service-type, the *-flags).
func buildSecretRequest(id string, conn settingsDict, setting string, hints []string, flags uint32, now, expires time.Time) core.SecretRequest {
	c := conn[settingConnection]
	req := core.SecretRequest{
		ID:             id,
		ConnectionUUID: vStr(c, "uuid"),
		ConnectionName: vStr(c, "id"),
		SettingName:    setting,
		RequestNew:     flags&getSecretsRequestNew != 0,
		UserRequested:  flags&getSecretsUserRequested != 0,
		CreatedAt:      now,
		ExpiresAt:      expires,
	}
	if w := conn[settingWifi]; w != nil {
		req.SSID = ssidString(vBytes(w, "ssid"))
	}
	var keys, messages []string
	for _, h := range hints {
		if strings.HasPrefix(h, vpnMessageHint) {
			if m := strings.TrimSpace(strings.TrimPrefix(h, vpnMessageHint)); m != "" {
				messages = append(messages, m)
			}
			continue
		}
		if h = strings.TrimSpace(h); h != "" {
			keys = append(keys, h)
		}
	}
	req.Message = strings.Join(messages, "\n")
	switch setting {
	case settingVPN:
		req.VPN = true
		req.VPNKind = core.VPNKindForServiceType(vStr(conn[settingVPN], "service-type"))
		if len(keys) == 0 {
			keys = []string{"password"}
		}
	case settingWireGuard:
		req.VPN = true
		req.VPNKind = "WireGuard"
		if len(keys) == 0 {
			keys = []string{"private-key"}
		}
	case settingWifiSecurity:
		if len(keys) == 0 {
			if vStr(conn[settingWifiSecurity], "key-mgmt") == "none" {
				keys = []string{"wep-key0"}
			} else {
				keys = []string{"psk"}
			}
		}
	default:
		if len(keys) == 0 {
			keys = []string{"password"}
		}
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		req.Fields = append(req.Fields, secretField(k, req.VPN))
	}
	return req
}

// secretField labels one requested key.
func secretField(key string, vpn bool) core.SecretField {
	f := core.SecretField{Key: key, Secret: true}
	switch {
	case key == "psk":
		f.Label = "Wi-Fi password"
	case strings.HasPrefix(key, "wep-key"):
		f.Label = "WEP key"
	case key == "password":
		if vpn {
			f.Label = "VPN password"
		} else {
			f.Label = "Password"
		}
	case key == "cert-pass":
		f.Label = "Certificate password"
	case key == "http-proxy-password":
		f.Label = "HTTP proxy password"
	case key == "challenge-response":
		f.Label = "One-time code / challenge response"
	case key == "private-key-password", key == "phase2-private-key-password":
		f.Label = "Private key password"
	case key == "private-key":
		f.Label = "Private key"
	case key == "preshared-key" || strings.HasSuffix(key, ".preshared-key"):
		f.Label = "Pre-shared key"
	case key == "username", key == "user-name", key == "identity", key == "user":
		f.Label = "Username"
		f.Secret = false
	default:
		f.Label = humanKey(key)
		if strings.Contains(key, "user") || strings.Contains(key, "identity") {
			f.Secret = false
		}
	}
	return f
}

// humanKey turns "http-proxy-password" into "Http proxy password".
func humanKey(key string) string {
	s := strings.ReplaceAll(strings.ReplaceAll(key, "-", " "), "_", " ")
	if s == "" {
		return key
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// flagsKey is the NM property carrying the secret flags of key.
func flagsKey(key string) string {
	if strings.HasPrefix(key, "wep-key") {
		return "wep-key-flags"
	}
	return key + "-flags"
}

// secretFlagsOf reads the profile's own flags for each requested key: 0 means
// system-owned (NM stores what we return), 1 agent-owned, 2 not-saved.
func secretFlagsOf(conn settingsDict, setting string, fields []core.SecretField) map[string]uint32 {
	out := map[string]uint32{}
	if setting == settingVPN {
		data := vStrMap(conn[settingVPN], "data")
		for _, f := range fields {
			if s, ok := data[f.Key+"-flags"]; ok {
				if n, err := strconv.ParseUint(s, 10, 32); err == nil {
					out[f.Key] = uint32(n)
				}
			}
		}
		return out
	}
	sec := conn[setting]
	for _, f := range fields {
		if sec == nil {
			continue
		}
		if _, ok := sec[flagsKey(f.Key)]; ok {
			out[f.Key] = vU32(sec, flagsKey(f.Key))
		}
	}
	return out
}

func needsPersist(flags map[string]uint32) bool {
	for _, v := range flags {
		if v != secretFlagsNone {
			return true
		}
	}
	return false
}

// secretsReply is GetSecrets' return value: the requested setting with the
// answered keys, system-owned (<key>-flags 0) when Save is set. VPN secrets
// go in vpn.secrets (a{ss}) with the flags in vpn.data.
func secretsReply(setting string, req core.SecretRequest, a core.SecretAnswer) settingsDict {
	if setting == settingVPN {
		secrets := map[string]string{}
		data := map[string]string{}
		for _, f := range req.Fields {
			v, ok := a.Secrets[f.Key]
			if !ok {
				continue
			}
			if f.Secret {
				secrets[f.Key] = v
				if a.Save {
					data[f.Key+"-flags"] = "0"
				}
			} else {
				data[f.Key] = v
			}
		}
		inner := props{"secrets": dbus.MakeVariant(secrets)}
		if len(data) > 0 {
			inner["data"] = dbus.MakeVariant(data)
		}
		return settingsDict{settingVPN: inner}
	}
	inner := props{}
	for _, f := range req.Fields {
		v, ok := a.Secrets[f.Key]
		if !ok {
			continue
		}
		inner[f.Key] = dbus.MakeVariant(v)
		if f.Secret && a.Save {
			inner[flagsKey(f.Key)] = dbus.MakeVariant(secretFlagsNone)
		}
	}
	return settingsDict{setting: inner}
}

func cloneRequest(r core.SecretRequest) core.SecretRequest {
	r.Fields = append([]core.SecretField(nil), r.Fields...)
	return r
}

func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("nm: agent: random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ---- Client glue ----

var _ core.SecretBroker = (*Client)(nil)

// WithSecretTimeout bounds how long a secret request stays answerable
// (default DefaultSecretTimeout).
func WithSecretTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.secretTimeout = d
		}
	}
}

// AgentRegistered reports whether NetworkManager accepted bnm's secret agent.
// When false, activations that need a secret the profile does not store fail
// with ErrNoSecrets as before.
func (c *Client) AgentRegistered() bool {
	return c.agent != nil && c.agent.isRegistered()
}

// SetSecretHandler installs the callback that publishes a new SecretRequest to
// the surfaces (the daemon). GetSecrets calls it on the D-Bus goroutine and
// then blocks until the request is answered, cancelled or expires; the
// callback must return promptly.
func (c *Client) SetSecretHandler(f func(core.SecretRequest)) {
	c.secretAgent().setNeeded(f)
}

// SetSecretResolvedHandler installs the callback run when a request ends,
// whichever way (answered, cancelled by a surface or by NM, timed out).
func (c *Client) SetSecretResolvedHandler(f func(id string, outcome core.SecretOutcome)) {
	c.secretAgent().setResolved(f)
}

// secretAgent returns the agent, creating a bus-less one for clients built
// without New (tests).
func (c *Client) secretAgent() *agent {
	c.ownerMu.Lock()
	defer c.ownerMu.Unlock()
	if c.agent == nil {
		c.agent = newAgent(nil, c.ctx, c.log, c.secretTimeout)
	}
	return c.agent
}

// Pending implements core.SecretBroker.
func (c *Client) Pending(ctx context.Context) ([]core.SecretRequest, error) {
	return c.secretAgent().pendingList(), nil
}

// Answer implements core.SecretBroker: ErrNotFound when the request is gone,
// ErrConflict when it was already answered or cancelled.
func (c *Client) Answer(ctx context.Context, id string, a core.SecretAnswer) error {
	return c.secretAgent().answer(id, a)
}

// Cancel implements core.SecretBroker.
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.secretAgent().cancel(id)
}

// registerAgent registers with AgentManager; failure is logged and the client
// carries on without prompts.
func (c *Client) registerAgent(ctx context.Context) {
	if c.agent == nil || c.agent.conn == nil {
		return
	}
	if err := c.agent.register(ctx); err != nil {
		c.log.Warn("nm: secret agent not registered; NetworkManager cannot prompt through bnm", "err", err)
		return
	}
	c.log.Info("nm: secret agent registered", "identifier", AgentIdentifier, "path", pathAgent)
}

// trustedSender says whether a GetSecrets caller is the current owner of
// org.freedesktop.NetworkManager.
func (c *Client) trustedSender(sender string) bool {
	owner := c.nmOwnerName()
	return owner != "" && sender == owner
}

// nmOwnerName is NM's unique bus name, cached from NameOwnerChanged.
func (c *Client) nmOwnerName() string {
	c.ownerMu.Lock()
	owner := c.nmOwner
	c.ownerMu.Unlock()
	if owner != "" {
		return owner
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	var name string
	if err := c.conn.BusObject().CallWithContext(ctx, ifaceDBus+".GetNameOwner", 0, busName).Store(&name); err != nil {
		c.log.Debug("nm: GetNameOwner failed", "err", err)
		return ""
	}
	c.setNMOwner(name)
	return name
}

func (c *Client) setNMOwner(name string) {
	c.ownerMu.Lock()
	c.nmOwner = name
	c.ownerMu.Unlock()
}

// persistSecrets stores an answered secret in the profile as system-owned when
// the profile marked it agent-owned or not-saved (NM only persists system-owned
// answers itself). Best effort: a failure is logged, the activation already
// has the secret.
func (c *Client) persistSecrets(connPath dbus.ObjectPath, setting string, req core.SecretRequest, a core.SecretAnswer, flags map[string]uint32) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 15*time.Second)
	defer cancel()
	op := "save secrets of " + req.ConnectionName
	err := c.editSettings(ctx, op, connPath, func(s settingsDict) error {
		sec := s[setting]
		if sec == nil {
			sec = props{}
			s[setting] = sec
		}
		if setting == settingVPN {
			data := map[string]string{}
			for k, v := range vStrMap(sec, "data") {
				data[k] = v
			}
			secrets := map[string]string{}
			for _, f := range req.Fields {
				v, ok := a.Secrets[f.Key]
				if !ok || !f.Secret {
					continue
				}
				secrets[f.Key] = v
				data[f.Key+"-flags"] = "0"
			}
			sec["data"] = dbus.MakeVariant(data)
			sec["secrets"] = dbus.MakeVariant(secrets)
			return nil
		}
		for _, f := range req.Fields {
			v, ok := a.Secrets[f.Key]
			if !ok || !f.Secret {
				continue
			}
			sec[f.Key] = dbus.MakeVariant(v)
			sec[flagsKey(f.Key)] = dbus.MakeVariant(secretFlagsNone)
		}
		return nil
	})
	if err != nil {
		c.log.Warn("nm: agent: could not store the answered secret in the profile", "profile", req.ConnectionName, "err", err)
		return
	}
	c.log.Info("nm: agent: stored answered secret in the profile", "profile", req.ConnectionName, "setting", setting)
}
