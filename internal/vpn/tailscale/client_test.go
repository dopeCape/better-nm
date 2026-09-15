package tailscale

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestClientStatusAndPrefs(t *testing.T) {
	d := newFakeDaemon(t)
	c := d.client()
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.BackendState != StateRunning || st.Version != "1.98.10" {
		t.Errorf("status = %+v", st)
	}
	if st.Self == nil || st.Self.ID != "nDDAL5zcst11CNTRL" || st.Self.DNSName != "homeserver.tailabc12.ts.net." {
		t.Errorf("self = %+v", st.Self)
	}
	if len(st.Peer) != 2 {
		t.Errorf("peers = %d, want 2", len(st.Peer))
	}
	p := st.Peer["nodekey:1aecad8ae657c9836987a4360d8c5d7adf180a26f1bab5d7b1ff203328654622"]
	if p == nil || !p.ExitNodeOption || p.Online || p.LastSeen.IsZero() || len(p.TailscaleIPs) != 2 {
		t.Errorf("peer = %+v", p)
	}
	if st.CurrentTailnet == nil || st.CurrentTailnet.MagicDNSSuffix != "tailabc12.ts.net" {
		t.Errorf("tailnet = %+v", st.CurrentTailnet)
	}
	if st.ExitNodeStatus != nil {
		t.Errorf("exit node status = %+v, want nil", st.ExitNodeStatus)
	}

	st2, err := c.StatusWithoutPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Peer) != 0 {
		t.Errorf("StatusWithoutPeers returned %d peers", len(st2.Peer))
	}

	pr, err := c.Prefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.WantRunning || !pr.CorpDNS || pr.ControlURL != "https://controlplane.tailscale.com" || pr.ExitNodeID != "" {
		t.Errorf("prefs = %+v", pr)
	}
}

func TestClientEditPrefs(t *testing.T) {
	d := newFakeDaemon(t)
	c := d.client()
	ctx := context.Background()

	pr, err := c.EditPrefs(ctx, MaskedPrefs{WantRunning: false, WantRunningSet: true, ExitNodeID: "nF7Q2vXCNTRL", ExitNodeIDSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if pr.WantRunning || pr.ExitNodeID != "nF7Q2vXCNTRL" {
		t.Errorf("edited prefs = %+v", pr)
	}
	calls := d.calls()
	last := calls[len(calls)-1]
	if last.Method != "PATCH" || last.Path != "/localapi/v0/prefs" {
		t.Errorf("last call = %+v", last)
	}
	want := `{"ExitNodeID":"nF7Q2vXCNTRL","ExitNodeIDSet":true,"WantRunningSet":true}`
	if last.Body != want {
		t.Errorf("body = %s, want %s", last.Body, want)
	}

	d.setWritable(false)
	_, err = c.EditPrefs(ctx, MaskedPrefs{})
	if !errors.Is(err, ErrAccessDenied) {
		t.Errorf("403 mapped to %v, want ErrAccessDenied", err)
	}
	if _, err := c.Prefs(ctx); err != nil {
		t.Errorf("reads must still work without operator: %v", err)
	}
}

func TestClientWriteEndpoints(t *testing.T) {
	d := newFakeDaemon(t)
	c := d.client()
	ctx := context.Background()

	if _, err := c.SetUseExitNode(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := c.StartLoginInteractive(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := c.Status(ctx)
	if st.AuthURL == "" {
		t.Error("login-interactive did not publish an AuthURL")
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if n := d.countCalls("POST", "/localapi/v0/set-use-exit-node-enabled?enabled=false"); n != 1 {
		t.Errorf("set-use-exit-node-enabled calls = %d", n)
	}
	if n := d.countCalls("POST", "/localapi/v0/logout"); n != 1 {
		t.Errorf("logout calls = %d", n)
	}

	d.setWritable(false)
	for name, fn := range map[string]func() error{
		"SetUseExitNode":        func() error { _, err := c.SetUseExitNode(ctx, true); return err },
		"StartLoginInteractive": func() error { return c.StartLoginInteractive(ctx) },
		"Logout":                func() error { return c.Logout(ctx) },
	} {
		if err := fn(); !errors.Is(err, ErrAccessDenied) {
			t.Errorf("%s without operator: %v, want ErrAccessDenied", name, err)
		}
	}
}

func TestClientUnavailable(t *testing.T) {
	ctx := context.Background()
	c := New(filepath.Join(t.TempDir(), "missing.sock"))
	_, err := c.Status(ctx)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("missing socket: %v, want ErrUnavailable", err)
	}

	// Socket file present but nobody listening (daemon died) -> ECONNREFUSED.
	d := newFakeDaemon(t)
	d.srv.Close()
	time.Sleep(20 * time.Millisecond)
	_, err = New(d.socket).Status(ctx)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("dead socket: %v, want ErrUnavailable", err)
	}
}

func TestClientHTTPError(t *testing.T) {
	d := newFakeDaemon(t)
	c := d.client()
	err := c.getJSON(context.Background(), "no-such-endpoint", &struct{}{})
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 404 {
		t.Errorf("404: %v", err)
	}
}

func TestClientWatchIPNBus(t *testing.T) {
	d := newFakeDaemon(t)
	c := d.client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan Notify, 8)
	done := make(chan error, 1)
	go func() {
		done <- c.WatchIPNBus(ctx, DefaultWatchMask, func(n Notify) bool {
			got <- n
			return true
		})
	}()

	first := <-got
	if first.StateName() != StateRunning || first.Prefs == nil || !first.Prefs.WantRunning || first.Health == nil {
		t.Errorf("initial notify = %+v", first)
	}
	d.notify <- `{"Version":"1.98.10","State":2,"BrowseToURL":"https://login.tailscale.com/a/abc"}`
	d.notify <- `{"Version":"1.98.10","ErrMessage":"boom","Health":{"Warnings":{"login-state":{"WarnableCode":"login-state","Severity":"medium","Title":"Not logged in","Text":"You are logged out."}}}}`
	n2 := <-got
	if n2.StateName() != StateNeedsLogin || n2.BrowseToURL == nil || *n2.BrowseToURL != "https://login.tailscale.com/a/abc" {
		t.Errorf("second notify = %+v", n2)
	}
	n3 := <-got
	if n3.ErrMessage == nil || *n3.ErrMessage != "boom" || len(n3.Health.Messages()) != 1 || n3.Health.Messages()[0] != "You are logged out." {
		t.Errorf("third notify = %+v", n3)
	}
	if calls := d.calls(); calls[0].Path != "/localapi/v0/watch-ipn-bus?mask=406" {
		t.Errorf("watch path = %s", calls[0].Path)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("watch after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchIPNBus did not return after cancel")
	}

	// Stream dropped by the daemon -> error so the caller reconnects.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	go func() {
		<-got // initial
		d.notify <- ""
	}()
	err := c.WatchIPNBus(ctx2, DefaultWatchMask, func(n Notify) bool { got <- n; return true })
	if err == nil {
		t.Error("dropped stream should return an error")
	}
}

func TestStateName(t *testing.T) {
	for n, want := range map[int]string{0: "NoState", 2: "NeedsLogin", 6: "Running", 9: "State(9)", -1: "State(-1)"} {
		if got := StateName(n); got != want {
			t.Errorf("StateName(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestClientRejectsWrongHost(t *testing.T) {
	// The fake mirrors tailscaled's Host check; make sure our client passes it
	// and that a bad Host really is refused, so the check is not vacuous.
	d := newFakeDaemon(t)
	c := d.client()
	req, err := c.newRequest(context.Background(), "GET", "status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("bad host status = %d, want 403", resp.StatusCode)
	}
}
