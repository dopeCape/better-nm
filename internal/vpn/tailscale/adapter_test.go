package tailscale

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

func newTestAdapter(t *testing.T, d *fakeDaemon) *Adapter {
	t.Helper()
	a := NewAdapter(d.client(), nil)
	a.username = "alice"
	a.MinBackoff = 10 * time.Millisecond
	a.MaxBackoff = 50 * time.Millisecond
	a.LoginTimeout = 2 * time.Second
	return a
}

func TestMapState(t *testing.T) {
	cases := map[string]core.VPNState{
		StateRunning:          core.VPNConnected,
		StateStarting:         core.VPNConnecting,
		StateNoState:          core.VPNConnecting,
		StateStopped:          core.VPNDisconnected,
		StateNeedsLogin:       core.VPNNeedsAuth,
		StateNeedsMachineAuth: core.VPNNeedsAuth,
		StateInUseOtherUser:   core.VPNNeedsSetup,
		"Bogus":               core.VPNError,
	}
	for in, want := range cases {
		if got := MapState(in); got != want {
			t.Errorf("MapState(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestAdapterListRunning(t *testing.T) {
	d := newFakeDaemon(t)
	a := newTestAdapter(t, d)
	ctx := context.Background()

	vpns, err := a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vpns) != 1 {
		t.Fatalf("got %d VPNs, want 1", len(vpns))
	}
	v := vpns[0]
	if v.ID != VPNID || v.Backend != core.BackendTailscale || v.Kind != "Tailscale" || v.Name != "Tailscale" {
		t.Errorf("identity = %+v", v)
	}
	if v.State != core.VPNConnected || !v.Writable || v.Detail != "" || v.AuthURL != "" {
		t.Errorf("state = %s writable=%v detail=%q auth=%q", v.State, v.Writable, v.Detail, v.AuthURL)
	}
	info := v.Tailscale
	if info == nil {
		t.Fatal("no TailscaleInfo")
	}
	if info.BackendState != StateRunning || info.Version != "1.98.10" || !info.OperatorOK || !info.AcceptDNS ||
		info.MagicDNS != "tailabc12.ts.net" || info.Tailnet != "user@example.com" || info.SelfName != "nixos-1" ||
		info.ControlURL != "https://controlplane.tailscale.com" || len(info.SelfIPs) != 2 || info.ExitNodeOn {
		t.Errorf("info = %+v", info)
	}
	if len(info.Peers) != 2 {
		t.Fatalf("peers = %+v", info.Peers)
	}
	// Sorted by name: nixos, xiaomi-pad-6.
	if info.Peers[0].Name != "nixos" || !info.Peers[0].ExitNodeOption || info.Peers[0].Online || info.Peers[0].LastSeen.IsZero() {
		t.Errorf("peer0 = %+v", info.Peers[0])
	}
	if info.Peers[1].Name != "xiaomi-pad-6" || info.Peers[1].HostName != "Xiaomi Pad 6" || info.Peers[1].OS != "android" || !info.Peers[1].Online {
		t.Errorf("peer1 = %+v", info.Peers[1])
	}
	// The writable probe is one empty PATCH.
	if n := d.countCalls("PATCH", "/localapi/v0/prefs"); n != 1 {
		t.Errorf("probe PATCH calls = %d, want 1", n)
	}
	for _, c := range d.calls() {
		if c.Method == "PATCH" && c.Body != "{}" {
			t.Errorf("probe body = %s, want an empty mask", c.Body)
		}
	}
	// Second List within ProbeTTL must not probe again.
	if _, err := a.List(ctx); err != nil {
		t.Fatal(err)
	}
	if n := d.countCalls("PATCH", "/localapi/v0/prefs"); n != 1 {
		t.Errorf("probe PATCH calls after cached List = %d, want 1", n)
	}
}

func TestAdapterListReadOnly(t *testing.T) {
	d := newFakeDaemon(t)
	d.setWritable(false)
	a := newTestAdapter(t, d)
	ctx := context.Background()

	vpns, err := a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := vpns[0]
	if v.State != core.VPNConnected || v.Writable || v.Tailscale.OperatorOK {
		t.Errorf("read-only: state=%s writable=%v", v.State, v.Writable)
	}
	if !strings.Contains(v.Detail, "sudo tailscale set --operator=alice") || !strings.Contains(v.Detail, `services.tailscale.extraSetFlags = [ "--operator=alice" ]`) {
		t.Errorf("detail = %q", v.Detail)
	}
	// Writes fail with the hint and ErrAccessDenied.
	err = a.Connect(ctx, VPNID)
	if !errors.Is(err, ErrAccessDenied) || !strings.Contains(err.Error(), "--operator=alice") {
		t.Errorf("Connect read-only: %v", err)
	}
	// The 403 is cached: the second List does not probe again.
	before := d.countCalls("PATCH", "/localapi/v0/prefs")
	a.List(ctx)
	if after := d.countCalls("PATCH", "/localapi/v0/prefs"); after != before {
		t.Errorf("cached 403 still probed: %d -> %d", before, after)
	}
}

func TestAdapterListStates(t *testing.T) {
	cases := []struct {
		backend string
		authURL string
		state   core.VPNState
		wantURL string
		detail  string
	}{
		{StateStopped, "", core.VPNDisconnected, "", ""},
		{StateStarting, "", core.VPNConnecting, "", ""},
		{StateNoState, "", core.VPNConnecting, "", ""},
		{StateNeedsLogin, "https://login.tailscale.com/a/xyz", core.VPNNeedsAuth, "https://login.tailscale.com/a/xyz", ""},
		{StateNeedsLogin, "", core.VPNNeedsAuth, "", "not logged in"},
		{StateNeedsMachineAuth, "", core.VPNNeedsAuth, "", "approved by a tailnet admin"},
		{StateInUseOtherUser, "", core.VPNNeedsSetup, "", "another user"},
	}
	for _, tc := range cases {
		t.Run(tc.backend+"/"+tc.authURL, func(t *testing.T) {
			d := newFakeDaemon(t)
			d.setStatus("BackendState", tc.backend)
			d.setStatus("AuthURL", tc.authURL)
			a := newTestAdapter(t, d)
			vpns, err := a.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			v := vpns[0]
			if v.State != tc.state || v.AuthURL != tc.wantURL || !strings.Contains(v.Detail, tc.detail) {
				t.Errorf("got state=%s auth=%q detail=%q; want %s %q %q", v.State, v.AuthURL, v.Detail, tc.state, tc.wantURL, tc.detail)
			}
			if v.Tailscale == nil || v.Tailscale.BackendState != tc.backend {
				t.Errorf("info = %+v", v.Tailscale)
			}
		})
	}
}

func TestAdapterListUnavailable(t *testing.T) {
	a := NewAdapter(New(filepath.Join(t.TempDir(), "none.sock")), nil)
	vpns, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v := vpns[0]
	if v.State != core.VPNUnavailable || v.Writable || !strings.Contains(v.Detail, "not running") || v.Tailscale != nil {
		t.Errorf("unavailable = %+v", v)
	}
}

func TestAdapterHeadscaleAndExitNode(t *testing.T) {
	d := newFakeDaemon(t)
	d.setPref("ControlURL", "https://headscale.example.org")
	d.setPref("ExitNodeID", "nF7Q2vXCNTRL")
	d.setPref("ExitNodeAllowLANAccess", true)
	d.setStatus("ExitNodeStatus", map[string]any{"ID": "nF7Q2vXCNTRL", "Online": true, "TailscaleIPs": []string{"100.64.0.11/32"}})
	a := newTestAdapter(t, d)
	vpns, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v := vpns[0]
	if v.Name != "Tailscale (headscale.example.org)" || v.Tailscale.ControlURL != "https://headscale.example.org" {
		t.Errorf("headscale name/url = %q %q", v.Name, v.Tailscale.ControlURL)
	}
	if !v.Tailscale.ExitNodeOn || v.Tailscale.ExitNodeID != "nF7Q2vXCNTRL" || v.Tailscale.ExitNodeName != "nixos" || !v.Tailscale.AllowLAN {
		t.Errorf("exit node info = %+v", v.Tailscale)
	}
}

func TestAdapterConnectDisconnect(t *testing.T) {
	d := newFakeDaemon(t)
	a := newTestAdapter(t, d)
	ctx := context.Background()

	if err := a.Disconnect(ctx, VPNID); err != nil {
		t.Fatal(err)
	}
	if d.pref("WantRunning") != false {
		t.Errorf("WantRunning after Disconnect = %v", d.pref("WantRunning"))
	}
	if err := a.Connect(ctx, VPNID); err != nil {
		t.Fatal(err)
	}
	if d.pref("WantRunning") != true {
		t.Errorf("WantRunning after Connect = %v", d.pref("WantRunning"))
	}
	if n := d.countCalls("POST", "/localapi/v0/login-interactive"); n != 0 {
		t.Errorf("Connect while Running started a login")
	}
	if err := a.Connect(ctx, "nope"); err == nil {
		t.Error("Connect with unknown id succeeded")
	}
	if err := a.Disconnect(ctx, "nope"); err == nil {
		t.Error("Disconnect with unknown id succeeded")
	}

	// Connect while NeedsLogin without a URL starts an interactive login.
	d.setStatus("BackendState", StateNeedsLogin)
	d.setStatus("AuthURL", "")
	if err := a.Connect(ctx, VPNID); err != nil {
		t.Fatal(err)
	}
	if n := d.countCalls("POST", "/localapi/v0/login-interactive"); n != 1 {
		t.Errorf("login-interactive calls = %d, want 1", n)
	}
	vpns, _ := a.List(ctx)
	if vpns[0].State != core.VPNNeedsAuth || vpns[0].AuthURL == "" {
		t.Errorf("after Connect: %+v", vpns[0])
	}
}

func TestAdapterTailscaleControl(t *testing.T) {
	d := newFakeDaemon(t)
	a := newTestAdapter(t, d)
	ctx := context.Background()
	var ctl core.TailscaleControl = a

	// By MagicDNS name.
	if err := ctl.SetExitNode(ctx, "nixos", true); err != nil {
		t.Fatal(err)
	}
	if d.pref("ExitNodeID") != "nF7Q2vXCNTRL" || d.pref("ExitNodeAllowLANAccess") != true || d.pref("ExitNodeIP") != "" {
		t.Errorf("prefs after SetExitNode = %v %v", d.pref("ExitNodeID"), d.pref("ExitNodeAllowLANAccess"))
	}
	// By IP, by ID, by full DNS name, by hostname with spaces.
	for _, ref := range []string{"100.64.0.11", "nF7Q2vXCNTRL", "nixos.tailabc12.ts.net", "NIXOS.tailabc12.ts.net."} {
		if err := ctl.SetExitNode(ctx, ref, false); err != nil {
			t.Errorf("SetExitNode(%q): %v", ref, err)
		}
	}
	// A peer without ExitNodeOption is refused.
	if err := ctl.SetExitNode(ctx, "xiaomi-pad-6", false); err == nil || !strings.Contains(err.Error(), "does not offer") {
		t.Errorf("SetExitNode on non-exit peer: %v", err)
	}
	if err := ctl.SetExitNode(ctx, "ghost", false); err == nil {
		t.Error("SetExitNode on unknown peer succeeded")
	}
	// Clear.
	if err := ctl.SetExitNode(ctx, "", false); err != nil {
		t.Fatal(err)
	}
	if d.pref("ExitNodeID") != "" || d.pref("ExitNodeAllowLANAccess") != false {
		t.Errorf("prefs after clear = %v", d.pref("ExitNodeID"))
	}

	d.setPref("ExitNodeID", "nF7Q2vXCNTRL")
	if err := ctl.UseExitNode(ctx, false); err != nil {
		t.Fatal(err)
	}
	if d.pref("ExitNodeID") != "" || d.pref("InternalExitNodePrior") != "nF7Q2vXCNTRL" {
		t.Errorf("UseExitNode(false): %v / %v", d.pref("ExitNodeID"), d.pref("InternalExitNodePrior"))
	}
	if err := ctl.UseExitNode(ctx, true); err != nil {
		t.Fatal(err)
	}
	if d.pref("ExitNodeID") != "nF7Q2vXCNTRL" {
		t.Errorf("UseExitNode(true): %v", d.pref("ExitNodeID"))
	}

	if err := ctl.SetAcceptDNS(ctx, false); err != nil {
		t.Fatal(err)
	}
	if d.pref("CorpDNS") != false {
		t.Errorf("CorpDNS = %v", d.pref("CorpDNS"))
	}

	d.setStatus("BackendState", StateNeedsLogin)
	url, err := ctl.Login(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if url != d.authURL {
		t.Errorf("Login url = %q, want %q", url, d.authURL)
	}
	if err := ctl.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if d.pref("LoggedOut") != true {
		t.Error("Logout did not log out")
	}

	// Everything is 403 without operator, with the hint attached.
	d.setWritable(false)
	for name, fn := range map[string]func() error{
		"SetExitNode":  func() error { return ctl.SetExitNode(ctx, "", false) },
		"UseExitNode":  func() error { return ctl.UseExitNode(ctx, true) },
		"SetAcceptDNS": func() error { return ctl.SetAcceptDNS(ctx, true) },
		"Login":        func() error { _, err := ctl.Login(ctx); return err },
		"Logout":       func() error { return ctl.Logout(ctx) },
	} {
		err := fn()
		if !errors.Is(err, ErrAccessDenied) || !strings.Contains(err.Error(), "--operator=alice") {
			t.Errorf("%s read-only: %v", name, err)
		}
	}
}

func TestAdapterLoginTimesOut(t *testing.T) {
	d := newFakeDaemon(t)
	d.authURL = "" // daemon never produces a URL
	d.setStatus("BackendState", StateNeedsLogin)
	a := newTestAdapter(t, d)
	a.LoginTimeout = 300 * time.Millisecond
	_, err := a.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no auth URL") {
		t.Errorf("Login without URL: %v", err)
	}
}

func TestAdapterWatch(t *testing.T) {
	d := newFakeDaemon(t)
	a := newTestAdapter(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := a.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recv := func() core.Change {
		t.Helper()
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatal("channel closed early")
			}
			return c
		case <-time.After(3 * time.Second):
			t.Fatal("no change received")
		}
		return core.Change{}
	}
	// Initial notify carries State+Prefs -> one change.
	if c := recv(); c.Kind != core.ChangeVPN || c.Path != VPNID {
		t.Errorf("initial change = %+v", c)
	}
	// A notify with nothing we care about is not forwarded; the next state change is.
	d.notify <- `{"Version":"1.98.10","Engine":{"RBytes":1}}`
	d.notify <- `{"Version":"1.98.10","State":4}`
	if c := recv(); c.Kind != core.ChangeVPN {
		t.Errorf("state change = %+v", c)
	}
	select {
	case c := <-ch:
		t.Errorf("unexpected extra change %+v", c)
	case <-time.After(50 * time.Millisecond):
	}

	// Drop the stream: one change for the drop, then a reconnect delivers the initial notify again.
	d.notify <- ""
	recv()
	recv()
	d.mu.Lock()
	w := d.watchers
	d.mu.Unlock()
	if w < 2 {
		t.Errorf("watchers = %d, want reconnect", w)
	}

	cancel()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}

func TestAdapterWatchDaemonDown(t *testing.T) {
	// No daemon: Watch must not spin, must emit exactly once for the outage,
	// and must stop with ctx.
	a := NewAdapter(New(filepath.Join(t.TempDir(), "none.sock")), nil)
	a.MinBackoff = 20 * time.Millisecond
	a.MaxBackoff = 40 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	ch, _ := a.Watch(ctx)
	n := 0
	for range ch {
		n++
	}
	if n != 1 {
		t.Errorf("changes while daemon down = %d", n)
	}
}

func TestPeerNameAndCustomControlHost(t *testing.T) {
	p := &PeerStatus{DNSName: "box.tailabc12.ts.net.", HostName: "Box"}
	if got := peerName(p, "tailabc12.ts.net"); got != "box" {
		t.Errorf("peerName = %q", got)
	}
	if got := peerName(&PeerStatus{HostName: "only-host"}, "x.ts.net"); got != "only-host" {
		t.Errorf("peerName fallback = %q", got)
	}
	if got := peerName(p, ""); got != "box.tailabc12.ts.net" {
		t.Errorf("peerName no suffix = %q", got)
	}
	for in, want := range map[string]string{
		"":                                   "",
		"https://controlplane.tailscale.com": "",
		"https://login.tailscale.com":        "",
		"https://hs.example.org:8080":        "hs.example.org:8080",
		"garbage":                            "garbage",
	} {
		if got := customControlHost(in); got != want {
			t.Errorf("customControlHost(%q) = %q, want %q", in, got, want)
		}
	}
}
