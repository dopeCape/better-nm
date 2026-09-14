package nm

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

// The secret agent is tested on a private dbus-daemon: the agent is exported
// on one connection, a second connection plays NetworkManager (it owns
// org.freedesktop.NetworkManager so the sender check passes, and exports a
// fake AgentManager) and calls GetSecrets, while the test answers through the
// Client's SecretBroker methods.

// startSessionBus forks a private dbus-daemon and returns its address, or skips.
func startSessionBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not installed")
	}
	out, err := exec.Command("dbus-daemon", "--session", "--print-address", "--print-pid", "--fork").Output()
	if err != nil {
		t.Skipf("dbus-daemon failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Skipf("unexpected dbus-daemon output %q", out)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	if err != nil {
		t.Skipf("bad pid %q", lines[1])
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	return strings.TrimSpace(lines[0])
}

func connectBus(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr, dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()))
	if err != nil {
		t.Fatalf("connect %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// fakeAgentManager records Register/Unregister calls.
type fakeAgentManager struct {
	mu         sync.Mutex
	registered []string
	caps       []uint32
	unregister int
}

func (m *fakeAgentManager) RegisterWithCapabilities(id string, caps uint32) *dbus.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered = append(m.registered, id)
	m.caps = append(m.caps, caps)
	return nil
}

func (m *fakeAgentManager) Unregister() *dbus.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unregister++
	return nil
}

// agentRig is the agent under test plus the connection playing NM.
type agentRig struct {
	addr string
	c    *Client
	nm   *dbus.Conn // owns org.freedesktop.NetworkManager
	mgr  *fakeAgentManager
	// needed receives every request the handler is given.
	needed chan core.SecretRequest
	// resolved receives (id, outcome) pairs.
	resolved chan [2]string
}

func newAgentRig(t *testing.T, timeout time.Duration) *agentRig {
	t.Helper()
	addr := startSessionBus(t)
	agentConn := connectBus(t, addr)
	nmConn := connectBus(t, addr)
	reply, err := nmConn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request %s: %v %v", busName, reply, err)
	}
	mgr := &fakeAgentManager{}
	if err := nmConn.Export(mgr, pathAgentMgr, ifaceAgentMgr); err != nil {
		t.Fatal(err)
	}

	c := newTestClient(t)
	seed(c)
	c.conn = agentConn
	c.secretTimeout = timeout
	c.agent = newAgent(agentConn, c.ctx, c.log, timeout)
	c.agent.trusted = c.trustedSender
	r := &agentRig{addr: addr, c: c, nm: nmConn, mgr: mgr, needed: make(chan core.SecretRequest, 8), resolved: make(chan [2]string, 8)}
	c.SetSecretHandler(func(req core.SecretRequest) { r.needed <- req })
	c.SetSecretResolvedHandler(func(id string, o core.SecretOutcome) { r.resolved <- [2]string{id, string(o)} })
	c.registerAgent(context.Background())
	return r
}

// getSecrets calls the exported agent from the NM connection.
func (r *agentRig) getSecrets(conn settingsDict, path dbus.ObjectPath, setting string, hints []string, flags uint32) (settingsDict, error) {
	var out settingsDict
	err := r.nm.Object(r.c.conn.Names()[0], pathAgent).Call(ifaceSecretAgent+".GetSecrets", 0, conn, path, setting, hints, flags).Store(&out)
	return out, err
}

type secretsResult struct {
	out settingsDict
	err error
}

func (r *agentRig) getSecretsAsync(conn settingsDict, path dbus.ObjectPath, setting string, hints []string, flags uint32) <-chan secretsResult {
	ch := make(chan secretsResult, 1)
	go func() {
		out, err := r.getSecrets(conn, path, setting, hints, flags)
		ch <- secretsResult{out, err}
	}()
	return ch
}

func (r *agentRig) waitNeeded(t *testing.T) core.SecretRequest {
	t.Helper()
	select {
	case req := <-r.needed:
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("no secret request reached the handler")
		return core.SecretRequest{}
	}
}

func (r *agentRig) waitResolved(t *testing.T, id string, want core.SecretOutcome) {
	t.Helper()
	select {
	case got := <-r.resolved:
		if got[0] != id || got[1] != string(want) {
			t.Fatalf("resolved = %v, want %s %s", got, id, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no resolution for %s", id)
	}
}

func dbusErrName(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name
	}
	return ""
}

func wifiConn(uuid, name, ssid string, pskFlags uint32) settingsDict {
	return settingsDict{
		settingConnection:   {"uuid": mv(uuid), "id": mv(name), "type": mv(typeWifi)},
		settingWifi:         {"ssid": mv([]byte(ssid)), "mode": mv("infrastructure")},
		settingWifiSecurity: {"key-mgmt": mv("wpa-psk"), "psk-flags": mv(pskFlags)},
	}
}

const (
	pConnCafe dbus.ObjectPath = "/org/freedesktop/NetworkManager/Settings/42"
	pConnVPN  dbus.ObjectPath = "/org/freedesktop/NetworkManager/Settings/43"

	// interactive is what NM sends for an activation a person started.
	interactive = getSecretsAllowInteraction | getSecretsUserRequested
)

func TestAgentRegistersAndIntrospects(t *testing.T) {
	r := newAgentRig(t, time.Second)
	if !r.c.AgentRegistered() {
		t.Fatal("agent must report registered")
	}
	r.mgr.mu.Lock()
	reg, caps := r.mgr.registered, r.mgr.caps
	r.mgr.mu.Unlock()
	if len(reg) != 1 || reg[0] != AgentIdentifier || caps[0] != agentCapVPNHints {
		t.Fatalf("RegisterWithCapabilities got %v %v", reg, caps)
	}
	var xml string
	if err := r.nm.Object(r.c.conn.Names()[0], pathAgent).Call(ifaceIntrospectable+".Introspect", 0).Store(&xml); err != nil {
		t.Fatalf("introspect: %v", err)
	}
	for _, want := range []string{ifaceSecretAgent, `name="GetSecrets"`, `type="a{sa{sv}}"`, `name="CancelGetSecrets"`, `name="SaveSecrets"`, `name="DeleteSecrets"`} {
		if !strings.Contains(xml, want) {
			t.Errorf("introspection lacks %s:\n%s", want, xml)
		}
	}
	// Unregister on shutdown.
	r.c.agent.unregister(context.Background())
	r.mgr.mu.Lock()
	n := r.mgr.unregister
	r.mgr.mu.Unlock()
	if n != 1 || r.c.AgentRegistered() {
		t.Fatalf("unregister calls = %d, registered = %v", n, r.c.AgentRegistered())
	}
	// A failed registration is logged, not fatal.
	c2 := newTestClient(t)
	c2.conn = r.c.conn
	c2.agent = newAgent(r.c.conn, c2.ctx, c2.log, time.Second)
	if err := r.nm.Export(nil, pathAgentMgr, ifaceAgentMgr); err != nil {
		t.Fatal(err)
	}
	c2.registerAgent(context.Background())
	if c2.AgentRegistered() {
		t.Fatal("registration against a missing AgentManager must fail")
	}
}

func TestAgentNoSecretsWithoutInteraction(t *testing.T) {
	r := newAgentRig(t, time.Second)
	_, err := r.getSecrets(wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, 0)
	if name := dbusErrName(err); name != errAgentNoSecrets {
		t.Fatalf("flags=0 must yield %s, got %v", errAgentNoSecrets, err)
	}
	if p, _ := r.c.Pending(context.Background()); len(p) != 0 {
		t.Fatalf("nothing must be pending, got %v", p)
	}
	// An autoconnect attempt (interaction allowed but not user-requested) is
	// declined at once rather than holding the device on an unanswered prompt.
	_, err = r.getSecrets(wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, getSecretsAllowInteraction|getSecretsRequestNew)
	if name := dbusErrName(err); name != errAgentNoSecrets {
		t.Fatalf("autoconnect must yield %s, got %v", errAgentNoSecrets, err)
	}
	select {
	case req := <-r.needed:
		t.Fatalf("autoconnect request reached the handler: %+v", req)
	default:
	}
	// Without a handler the agent also declines instead of blocking.
	r.c.SetSecretHandler(nil)
	_, err = r.getSecrets(wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, interactive)
	if name := dbusErrName(err); name != errAgentNoSecrets {
		t.Fatalf("no handler must yield %s, got %v", errAgentNoSecrets, err)
	}
}

func TestAgentRefusesStrangers(t *testing.T) {
	r := newAgentRig(t, time.Second)
	stranger := connectBus(t, r.addr)
	var out settingsDict
	err := stranger.Object(r.c.conn.Names()[0], pathAgent).Call(ifaceSecretAgent+".GetSecrets", 0,
		wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, interactive).Store(&out)
	if name := dbusErrName(err); name != errAgentFailed {
		t.Fatalf("a caller that is not NM must get %s, got %v", errAgentFailed, err)
	}
	select {
	case req := <-r.needed:
		t.Fatalf("stranger's request reached the handler: %+v", req)
	default:
	}
}

func TestAgentAnswerWifi(t *testing.T) {
	r := newAgentRig(t, 5*time.Second)
	ctx := context.Background()
	res := r.getSecretsAsync(wifiConn("uuid-cafe", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"},
		interactive)
	req := r.waitNeeded(t)
	if req.ID == "" || len(req.ID) != 16 {
		t.Fatalf("id = %q", req.ID)
	}
	if req.ConnectionUUID != "uuid-cafe" || req.ConnectionName != "Cafe" || req.SSID != "Cafe" || req.VPN ||
		req.SettingName != settingWifiSecurity || !req.UserRequested || req.RequestNew {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Fields) != 1 || req.Fields[0] != (core.SecretField{Key: "psk", Label: "Wi-Fi password", Secret: true}) {
		t.Fatalf("fields = %+v", req.Fields)
	}
	if req.ExpiresAt.Sub(req.CreatedAt) != 5*time.Second {
		t.Fatalf("expiry = %v", req.ExpiresAt.Sub(req.CreatedAt))
	}
	pending, err := r.c.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != req.ID {
		t.Fatalf("Pending = %+v %v", pending, err)
	}
	if !r.c.agent.pendingFor(pConnCafe) || r.c.agent.pendingFor(pConnVPN) {
		t.Fatal("pendingFor must track the connection path")
	}

	// A wrong id is not found; an answer without any requested key is invalid.
	if err := r.c.Answer(ctx, "nope", core.SecretAnswer{Secrets: map[string]string{"psk": "x"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := r.c.Answer(ctx, req.ID, core.SecretAnswer{Secrets: map[string]string{"other": "x"}}); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("empty answer must be refused: %v", err)
	}
	if err := r.c.Answer(ctx, req.ID, core.SecretAnswer{Secrets: map[string]string{"psk": "hunter22"}, Save: true}); err != nil {
		t.Fatal(err)
	}
	r.waitResolved(t, req.ID, core.SecretAnswered)
	got := <-res
	if got.err != nil {
		t.Fatalf("GetSecrets: %v", got.err)
	}
	sec := got.out[settingWifiSecurity]
	if sec == nil || len(got.out) != 1 {
		t.Fatalf("reply = %v", got.out)
	}
	if sec["psk"].Value() != "hunter22" || sec["psk"].Signature().String() != "s" {
		t.Fatalf("psk = %v", sec["psk"])
	}
	if sec["psk-flags"].Value() != uint32(0) || sec["psk-flags"].Signature().String() != "u" {
		t.Fatalf("psk-flags = %v", sec["psk-flags"])
	}
	// Resolved requests leave Pending and refuse a second answer or cancel.
	if p, _ := r.c.Pending(ctx); len(p) != 0 {
		t.Fatalf("pending after answer = %v", p)
	}
	if err := r.c.Answer(ctx, req.ID, core.SecretAnswer{Secrets: map[string]string{"psk": "again"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second answer must be ErrConflict, got %v", err)
	}
	if err := r.c.Cancel(ctx, req.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel after answer must be ErrConflict, got %v", err)
	}
	if r.c.agent.pendingFor(pConnCafe) {
		t.Fatal("pendingFor after answer")
	}

	// Save=false leaves the flags out; WEP maps to the shared wep-key-flags.
	wep := wifiConn("uuid-wep", "Wep", "Wep", 0)
	wep[settingWifiSecurity] = props{"key-mgmt": mv("none"), "wep-key-flags": mv(uint32(0))}
	res = r.getSecretsAsync(wep, pConnCafe, settingWifiSecurity, nil, interactive)
	req = r.waitNeeded(t)
	if len(req.Fields) != 1 || req.Fields[0].Key != "wep-key0" || req.Fields[0].Label != "WEP key" {
		t.Fatalf("wep fields = %+v", req.Fields)
	}
	if err := r.c.Answer(ctx, req.ID, core.SecretAnswer{Secrets: map[string]string{"wep-key0": "abcde"}}); err != nil {
		t.Fatal(err)
	}
	got = <-res
	if got.err != nil || got.out[settingWifiSecurity]["wep-key0"].Value() != "abcde" {
		t.Fatalf("wep reply = %v %v", got.out, got.err)
	}
	if _, has := got.out[settingWifiSecurity]["wep-key-flags"]; has {
		t.Fatalf("Save=false must not send flags: %v", got.out)
	}
}

func TestAgentAnswerVPNAndPersist(t *testing.T) {
	r := newAgentRig(t, 5*time.Second)
	ctx := context.Background()
	persisted := make(chan map[string]uint32, 1)
	r.c.agent.persist = func(connPath dbus.ObjectPath, setting string, req core.SecretRequest, a core.SecretAnswer, flags map[string]uint32) {
		if connPath != pConnVPN || setting != settingVPN || a.Secrets["password"] != "s3cret" {
			t.Errorf("persist got %s %s %+v", connPath, setting, a)
		}
		persisted <- flags
	}
	conn := settingsDict{
		settingConnection: {"uuid": mv("uuid-ovpn"), "id": mv("Office VPN"), "type": mv(typeVPN)},
		settingVPN: {
			"service-type": mv("org.freedesktop.NetworkManager.openvpn"),
			"data":         mv(map[string]string{"remote": "vpn.example", "password-flags": "1", "username": "tejas"}),
		},
	}
	hints := []string{"x-vpn-message:Enter your one-time code", "challenge-response", "password", "username"}
	res := r.getSecretsAsync(conn, pConnVPN, settingVPN, hints, interactive|getSecretsRequestNew)
	req := r.waitNeeded(t)
	if !req.VPN || req.VPNKind != "OpenVPN" || req.Message != "Enter your one-time code" || !req.RequestNew || !req.UserRequested {
		t.Fatalf("vpn request = %+v", req)
	}
	wantFields := []core.SecretField{
		{Key: "challenge-response", Label: "One-time code / challenge response", Secret: true},
		{Key: "password", Label: "VPN password", Secret: true},
		{Key: "username", Label: "Username", Secret: false},
	}
	if len(req.Fields) != len(wantFields) {
		t.Fatalf("fields = %+v", req.Fields)
	}
	for i := range wantFields {
		if req.Fields[i] != wantFields[i] {
			t.Fatalf("field %d = %+v, want %+v", i, req.Fields[i], wantFields[i])
		}
	}
	ans := core.SecretAnswer{Secrets: map[string]string{"challenge-response": "123456", "password": "s3cret", "username": "tejas"}, Save: true}
	if err := r.c.Answer(ctx, req.ID, ans); err != nil {
		t.Fatal(err)
	}
	got := <-res
	if got.err != nil {
		t.Fatalf("GetSecrets: %v", got.err)
	}
	vpn := got.out[settingVPN]
	secrets, ok := vpn["secrets"].Value().(map[string]string)
	if !ok || vpn["secrets"].Signature().String() != "a{ss}" {
		t.Fatalf("vpn.secrets = %v", vpn["secrets"])
	}
	if secrets["challenge-response"] != "123456" || secrets["password"] != "s3cret" || len(secrets) != 2 {
		t.Fatalf("vpn.secrets = %v", secrets)
	}
	data, _ := vpn["data"].Value().(map[string]string)
	if data["password-flags"] != "0" || data["challenge-response-flags"] != "0" || data["username"] != "tejas" {
		t.Fatalf("vpn.data = %v", data)
	}
	select {
	case flags := <-persisted:
		if flags["password"] != 1 {
			t.Fatalf("persist flags = %v", flags)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent-owned secret with Save must be persisted")
	}

	// A system-owned secret (flags 0) is stored by NM itself: no persist call;
	// and with no hints a VPN asks for the password.
	conn[settingVPN]["data"] = mv(map[string]string{"password-flags": "0"})
	res = r.getSecretsAsync(conn, pConnVPN, settingVPN, nil, interactive)
	req = r.waitNeeded(t)
	if len(req.Fields) != 1 || req.Fields[0].Key != "password" || req.Message != "" {
		t.Fatalf("default vpn fields = %+v", req.Fields)
	}
	if err := r.c.Answer(ctx, req.ID, core.SecretAnswer{Secrets: map[string]string{"password": "pw"}, Save: true}); err != nil {
		t.Fatal(err)
	}
	if got := <-res; got.err != nil {
		t.Fatal(got.err)
	}
	select {
	case <-persisted:
		t.Fatal("system-owned secret must not trigger persist")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAgentCancelAndTimeout(t *testing.T) {
	r := newAgentRig(t, 5*time.Second)
	ctx := context.Background()

	// Cancelled by a surface.
	res := r.getSecretsAsync(wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, interactive)
	req := r.waitNeeded(t)
	if err := r.c.Cancel(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	r.waitResolved(t, req.ID, core.SecretCancelled)
	if got := <-res; dbusErrName(got.err) != errAgentUserCanceled {
		t.Fatalf("user cancel must yield %s, got %v", errAgentUserCanceled, got.err)
	}
	if err := r.c.Cancel(ctx, req.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second cancel: %v", err)
	}
	if err := r.c.Cancel(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel unknown: %v", err)
	}

	// Cancelled by NM (CancelGetSecrets).
	res = r.getSecretsAsync(wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, interactive)
	req = r.waitNeeded(t)
	if err := r.nm.Object(r.c.conn.Names()[0], pathAgent).Call(ifaceSecretAgent+".CancelGetSecrets", 0, pConnCafe, settingWifiSecurity).Err; err != nil {
		t.Fatal(err)
	}
	r.waitResolved(t, req.ID, core.SecretCancelled)
	if got := <-res; dbusErrName(got.err) != errAgentAgentCanceled {
		t.Fatalf("NM cancel must yield %s, got %v", errAgentAgentCanceled, got.err)
	}
	if p, _ := r.c.Pending(ctx); len(p) != 0 {
		t.Fatalf("pending after NM cancel = %v", p)
	}

	// SaveSecrets / DeleteSecrets are accepted no-ops.
	for _, m := range []string{"SaveSecrets", "DeleteSecrets"} {
		if err := r.nm.Object(r.c.conn.Names()[0], pathAgent).Call(ifaceSecretAgent+"."+m, 0, wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe).Err; err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}

	// Expiry.
	r.c.agent.setTimeout(200 * time.Millisecond)
	start := time.Now()
	res = r.getSecretsAsync(wifiConn("u1", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, interactive)
	req = r.waitNeeded(t)
	if req.ExpiresAt.Sub(req.CreatedAt) != 200*time.Millisecond {
		t.Fatalf("expiry = %v", req.ExpiresAt.Sub(req.CreatedAt))
	}
	r.waitResolved(t, req.ID, core.SecretTimeout)
	got := <-res
	if dbusErrName(got.err) != errAgentUserCanceled || time.Since(start) < 200*time.Millisecond || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout after %v: %v", time.Since(start), got.err)
	}
	if err := r.c.Answer(ctx, req.ID, core.SecretAnswer{Secrets: map[string]string{"psk": "late"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("answer after timeout: %v", err)
	}
}

func TestAgentPendingExtendsActivationWait(t *testing.T) {
	r := newAgentRig(t, 5*time.Second)
	c := r.c
	c.activateTimeout = 150 * time.Millisecond
	ac := dbus.ObjectPath("/org/freedesktop/NetworkManager/ActiveConnection/9")
	c.objs[ac] = map[string]props{ifaceActive: {"State": mv(activeActivating), "Connection": mv(pConnCafe), "Devices": mv([]dbus.ObjectPath{pWifi})}}

	res := r.getSecretsAsync(wifiConn("uuid-cafe", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, interactive)
	req := r.waitNeeded(t)

	errc := make(chan error, 1)
	go func() { errc <- c.waitLoop(context.Background(), "connect wifi Cafe", ac) }()
	waitForWaiter(t, c, ac)
	select {
	case err := <-errc:
		t.Fatalf("wait ended while a prompt was open: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := c.Answer(context.Background(), req.ID, core.SecretAnswer{Secrets: map[string]string{"psk": "hunter22"}}); err != nil {
		t.Fatal(err)
	}
	if got := <-res; got.err != nil {
		t.Fatal(got.err)
	}
	// Activation completes after the answer, well past the nominal timeout.
	time.Sleep(100 * time.Millisecond)
	c.handle(&dbus.Signal{Path: ac, Name: ifaceActive + ".StateChanged", Body: []any{activeActivated, activeReasonNone}})
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("activated after prompt must be nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not end after activation")
	}

	// Once the prompt resolves without progress, the normal timeout applies.
	res = r.getSecretsAsync(wifiConn("uuid-cafe", "Cafe", "Cafe", 0), pConnCafe, settingWifiSecurity, []string{"psk"}, interactive)
	req = r.waitNeeded(t)
	go func() { errc <- c.waitLoop(context.Background(), "connect wifi Cafe", ac) }()
	waitForWaiter(t, c, ac)
	time.Sleep(300 * time.Millisecond)
	if err := c.Cancel(context.Background(), req.ID); err != nil {
		t.Fatal(err)
	}
	<-res
	start := time.Now()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("want ErrTimeout, got %v", err)
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Fatalf("timeout took %v after the prompt resolved", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeout after the prompt resolved")
	}
}

func TestBuildSecretRequestDefaults(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name    string
		setting string
		conn    settingsDict
		hints   []string
		flags   uint32
		want    []core.SecretField
		vpn     bool
		kind    string
	}{
		{"wifi psk", settingWifiSecurity, wifiConn("u", "n", "s", 0), nil, 1,
			[]core.SecretField{{Key: "psk", Label: "Wi-Fi password", Secret: true}}, false, ""},
		{"802-1x", "802-1x", wifiConn("u", "n", "s", 0), nil, 1,
			[]core.SecretField{{Key: "password", Label: "Password", Secret: true}}, false, ""},
		{"802-1x hints", "802-1x", wifiConn("u", "n", "s", 0), []string{"private-key-password", "identity"}, 1,
			[]core.SecretField{{Key: "private-key-password", Label: "Private key password", Secret: true}, {Key: "identity", Label: "Username", Secret: false}}, false, ""},
		{"wireguard", settingWireGuard, settingsDict{settingConnection: {"uuid": mv("u"), "id": mv("wg0"), "type": mv(typeWireGuard)}}, nil, 1,
			[]core.SecretField{{Key: "private-key", Label: "Private key", Secret: true}}, true, "WireGuard"},
		{"vpn unknown hint", settingVPN, settingsDict{settingConnection: {"uuid": mv("u"), "id": mv("v")}, settingVPN: {"service-type": mv("org.freedesktop.NetworkManager.openconnect")}},
			[]string{"cookie", "cert-pass", "http-proxy-password", "cookie"}, 1,
			[]core.SecretField{{Key: "cookie", Label: "Cookie", Secret: true}, {Key: "cert-pass", Label: "Certificate password", Secret: true}, {Key: "http-proxy-password", Label: "HTTP proxy password", Secret: true}}, true, "OpenConnect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := buildSecretRequest("id", tc.conn, tc.setting, tc.hints, tc.flags, now, now.Add(time.Minute))
			if req.VPN != tc.vpn || req.VPNKind != tc.kind {
				t.Fatalf("vpn=%v kind=%q", req.VPN, req.VPNKind)
			}
			if len(req.Fields) != len(tc.want) {
				t.Fatalf("fields = %+v, want %+v", req.Fields, tc.want)
			}
			for i := range tc.want {
				if req.Fields[i] != tc.want[i] {
					t.Fatalf("field %d = %+v, want %+v", i, req.Fields[i], tc.want[i])
				}
			}
		})
	}
	if flagsKey("wep-key1") != "wep-key-flags" || flagsKey("psk") != "psk-flags" {
		t.Fatal("flagsKey")
	}
}
