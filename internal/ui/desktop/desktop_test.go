package desktop

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
	"github.com/dopeCape/better-nm/internal/fake"
)

// rig is an in-process bnmd (fake backends) behind the real API server and
// client, plus the desktop App on Fyne's test driver.
type rig struct {
	t     *testing.T
	a     *App
	c     *client.Client
	d     *daemon.Daemon
	nm    *fake.NM
	ts    *fake.VPNAdapter
	mon   *fake.Monitor
	speed *fake.SpeedTester
	notif *fake.Notifier
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bnmui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, nm: fake.NewNM(), ts: fake.NewTailscale(), mon: fake.NewMonitor(), speed: fake.NewSpeedTester(), notif: fake.NewNotifier()}
	dir := shortTempDir(t)
	socket := filepath.Join(dir, "bnmd.sock")
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	d, err := daemon.New(daemon.Options{
		NM:                r.nm,
		VPN:               fake.NewVPNRegistry(r.ts, fake.NewWireGuard(), fake.NewNMVPN()),
		Monitor:           r.mon,
		Store:             fake.NewStore(),
		Speed:             r.speed,
		Notifier:          r.notif,
		Diag:              fake.NewDiag(),
		WireGuard:         &fake.WireGuardImporter{NM: r.nm},
		Config:            config.Default(),
		ConfigPath:        filepath.Join(dir, "config.toml"),
		Logger:            slog.New(slog.DiscardHandler),
		Version:           "test-1",
		Debounce:          20 * time.Millisecond,
		ConnectivityGrace: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	sock, err := daemon.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	serveDone := make(chan error, 1)
	go func() { runDone <- d.Run(ctx) }()
	srv := api.New(d, api.WithLogger(slog.New(slog.DiscardHandler)), api.WithHeartbeat(40*time.Millisecond))
	go func() { serveDone <- srv.Serve(ctx, sock.Listener()) }()

	c, err := client.New(client.WithSocket(socket), client.WithAutoStart(false))
	if err != nil {
		t.Fatal(err)
	}
	r.c = c

	fy := test.NewApp()
	off := false
	r.a = New(fy, c, Options{
		Logger: slog.New(slog.DiscardHandler),
		Tray:   &off,
		InstallService: func(context.Context) (string, error) {
			return "stubbed install", nil
		},
	})
	r.a.Start()
	t.Cleanup(func() {
		r.a.cancel()
		r.a.waitIdle(5 * time.Second)
		c.Close()
		cancel()
		for _, ch := range []chan error{runDone, serveDone} {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Error("shutdown timed out")
			}
		}
		sock.Close()
	})
	r.idle()
	return r
}

// idle waits for every background fetch and UI update to land.
func (r *rig) idle() {
	r.t.Helper()
	if !r.a.waitIdle(10 * time.Second) {
		r.t.Fatal("app did not go idle")
	}
}

// settle waits for the app to go idle twice with a gap, so refreshes the
// daemon's debounced change hints trigger have also landed. Needed before
// Tailscale writes: fake.VPNAdapter.List shallow-copies the VPN structs, so a
// concurrent GET /vpn would race SetExitNode through the shared *TailscaleInfo.
func (r *rig) settle() {
	r.t.Helper()
	r.idle()
	time.Sleep(150 * time.Millisecond)
	r.idle()
}

// ui runs fn under the UI lock so reads and taps never race a background render.
func (r *rig) ui(fn func()) {
	r.a.uiMu.Lock()
	defer r.a.uiMu.Unlock()
	fn()
}

// waitFor polls cond (under the UI lock) until it holds or the deadline passes.
func (r *rig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ok bool
		r.ui(func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *rig) overview() *overviewView { return r.a.views[SectionOverview].(*overviewView) }
func (r *rig) wifi() *wifiView         { return r.a.views[SectionWifi].(*wifiView) }
func (r *rig) vpn() *vpnView           { return r.a.views[SectionVPN].(*vpnView) }
func (r *rig) quality() *qualityView   { return r.a.views[SectionQuality].(*qualityView) }
func (r *rig) speedView() *speedView   { return r.a.views[SectionSpeed].(*speedView) }
func (r *rig) settings() *settingsView { return r.a.views[SectionSettings].(*settingsView) }
func (r *rig) devices() *devicesView   { return r.a.views[SectionDevices].(*devicesView) }
func (r *rig) advanced() *advancedView { return r.a.views[SectionAdvanced].(*advancedView) }

func TestOverviewShowsConnection(t *testing.T) {
	r := newRig(t)
	r.ui(func() {
		ov := r.overview()
		if got := ov.conn.values[0].Text; got != fake.HomeSSID {
			t.Errorf("name = %q", got)
		}
		if got := ov.conn.values[1].Text; got != "192.168.1.42/24" {
			t.Errorf("ip = %q", got)
		}
		if got := ov.conn.values[2].Text; got != "192.168.1.1" {
			t.Errorf("gateway = %q", got)
		}
		if !strings.HasPrefix(ov.conn.values[4].Text, "82%") {
			t.Errorf("signal = %q", ov.conn.values[4].Text)
		}
		if len(ov.vpnRows.Objects) != 3 {
			t.Errorf("vpn rows = %d", len(ov.vpnRows.Objects))
		}
		if r.a.header.name.Text != fake.HomeSSID || r.a.header.ip.Text != "192.168.1.42/24" {
			t.Errorf("header = %q %q", r.a.header.name.Text, r.a.header.ip.Text)
		}
		if !strings.HasPrefix(r.a.header.verdict.Text, "Quality: ") {
			t.Errorf("verdict = %q", r.a.header.verdict.Text)
		}
	})
}

func TestWifiListAndDetail(t *testing.T) {
	r := newRig(t)
	w := r.wifi()
	r.ui(func() {
		if len(w.filtered) != 4 {
			t.Fatalf("wifi rows = %d", len(w.filtered))
		}
		if w.filtered[0].SSID != fake.HomeSSID {
			t.Errorf("active network should sort first, got %s", w.filtered[0].SSID)
		}
		if !w.enabled.Checked {
			t.Error("wifi switch should be on")
		}
		// row template renders the SSID and strength
		tmpl := w.newRow()
		w.rows[tmpl.box] = tmpl
		w.updateRow(0, tmpl.box)
		if tmpl.name.Text != fake.HomeSSID || tmpl.pct.Text != "82" || tmpl.tag.Text != "connected" {
			t.Errorf("row = %q %q %q", tmpl.name.Text, tmpl.pct.Text, tmpl.tag.Text)
		}
	})
	// a secured, unknown network asks for a password
	sel := func(ssid string) {
		r.ui(func() {
			for i, n := range w.filtered {
				if n.SSID == ssid {
					w.list.Select(i)
				}
			}
		})
	}
	sel(fake.NeighbourSSD)
	r.ui(func() {
		if w.title.Text != fake.NeighbourSSD {
			t.Fatalf("detail title = %q", w.title.Text)
		}
		if !containsObject(w.detail, w.password) {
			t.Error("password entry should be shown for a WPA3 network")
		}
		if containsObject(w.detail, w.username) {
			t.Error("username entry should not be shown for a PSK network")
		}
		if containsObject(w.detail, w.ipBox) {
			t.Error("IP form only applies to saved networks")
		}
	})
	// an open network connects without a password
	sel(fake.CafeSSID)
	r.ui(func() {
		if w.title.Text != fake.CafeSSID {
			t.Fatalf("detail title = %q", w.title.Text)
		}
		if containsObject(w.detail, w.password) {
			t.Error("open network must not ask for a password")
		}
		if w.connectBtn.Text != "Connect" {
			t.Errorf("button = %q", w.connectBtn.Text)
		}
	})
	// enterprise networks ask for a username too
	sel(fake.OfficeSSID)
	r.ui(func() {
		// Office is known: no username, but the password entry stays available so
		// a rejected or missing saved password can be replaced without forgetting.
		if containsObject(w.detail, w.username) {
			t.Error("saved network should not ask for a username")
		}
		if !containsObject(w.detail, w.password) || !strings.Contains(w.password.PlaceHolder, "saved") {
			t.Errorf("saved network should offer a new-password entry, placeholder %q", w.password.PlaceHolder)
		}
		if !containsObject(w.detail, w.ipBox) || !containsObject(w.detail, w.autoconnect) {
			t.Error("saved network should show autoconnect and the IP form")
		}
	})

	// connect to the neighbour with a password: the fake records it as primary
	sel(fake.NeighbourSSD)
	r.ui(func() {
		w.password.SetText("hunter22")
		test.Tap(w.connectBtn)
	})
	r.idle()
	r.waitFor("header to follow the new network", func() bool { return r.a.header.name.Text == fake.NeighbourSSD })
	st, _ := r.c.Status(context.Background())
	if st.NetworkKey != "wifi:"+fake.NeighbourSSD {
		t.Errorf("daemon key = %s", st.NetworkKey)
	}

	// inline error with hint when the daemon refuses
	r.nm.Fail("ConnectWifi", core.Errorf(core.KindPermission, "ask your admin for network-control", "nm: not allowed"))
	sel(fake.CafeSSID)
	r.ui(func() { test.Tap(w.connectBtn) })
	r.idle()
	r.ui(func() {
		if !w.actErr.Visible() || !strings.Contains(w.actErr.Text, "network-control") {
			t.Errorf("inline error = %q visible=%v", w.actErr.Text, w.actErr.Visible())
		}
	})
	r.nm.Fail("ConnectWifi", nil)

	// the IP form writes through SetProfileIP
	sel(fake.OfficeSSID)
	r.ui(func() {
		w.ipMethod.SetSelected("Manual")
		w.ipAddrs.SetText("10.0.0.7/24")
		w.ipGateway.SetText("10.0.0.1")
		w.ipDNS.SetText("1.1.1.1, 9.9.9.9")
		test.Tap(w.ipApply)
	})
	r.idle()
	p, err := r.c.Profile(context.Background(), fake.OfficeUUID)
	if err != nil || p.IPv4.Method != core.IPManual || len(p.IPv4.DNS) != 2 || p.IPv4.Gateway != "10.0.0.1" {
		t.Errorf("profile ip = %+v %v", p.IPv4, err)
	}
	// autoconnect toggle
	r.ui(func() { test.Tap(w.autoconnect) })
	r.idle()
	p, _ = r.c.Profile(context.Background(), fake.OfficeUUID)
	if p.Autoconnect {
		t.Error("autoconnect should be off after toggling")
	}

	// Forget asks first: one tap must not delete the profile
	r.ui(func() { test.Tap(w.forgetBtn) })
	r.idle()
	if _, err := r.c.Profile(context.Background(), fake.OfficeUUID); err != nil {
		t.Fatalf("a single tap on Forget deleted the profile: %v", err)
	}
	r.ui(func() {
		if w.confirm == nil || r.a.win.Canvas().Overlays().Top() == nil {
			t.Fatal("Forget should open a confirmation dialog")
		}
		w.confirm.Dismiss()
	})
	r.idle()
	if _, err := r.c.Profile(context.Background(), fake.OfficeUUID); err != nil {
		t.Fatalf("Keep deleted the profile: %v", err)
	}
	r.ui(func() {
		if w.confirm != nil {
			t.Error("the dialog reference should clear on dismiss")
		}
		test.Tap(w.forgetBtn)
	})
	r.ui(func() { w.confirm.Confirm() })
	r.idle()
	if _, err := r.c.Profile(context.Background(), fake.OfficeUUID); err == nil {
		t.Error("confirming Forget should delete the profile")
	}
}

func TestVPNSwitchConnects(t *testing.T) {
	r := newRig(t)
	v := r.vpn()
	r.ui(func() {
		if len(v.vpns) != 3 || len(v.switches) != 3 {
			t.Fatalf("vpns = %d switches = %d", len(v.vpns), len(v.switches))
		}
		if !v.tsBox.Visible() {
			t.Error("tailscale section should be visible")
		}
		if got := v.tsExit.Options; len(got) != 2 || got[1] != "homeserver" {
			t.Errorf("exit node options = %v", got)
		}
		if !v.tsDNS.Checked {
			t.Error("accept DNS should reflect the fake")
		}
		test.Tap(v.switches["tailscale"])
	})
	r.idle()
	vs, _ := r.ts.List(context.Background())
	if vs[0].State != core.VPNConnected {
		t.Errorf("fake tailscale state = %s", vs[0].State)
	}
	r.waitFor("vpn row to show connected", func() bool {
		l, ok := v.states["tailscale"]
		return ok && strings.Contains(l.Text, "connected")
	})
	// exit node picker
	r.settle()
	r.ui(func() { v.tsExit.SetSelected("homeserver") })
	r.settle()
	vs, _ = r.ts.List(context.Background())
	if !vs[0].Tailscale.ExitNodeOn || vs[0].Tailscale.ExitNodeID != "n1" {
		t.Errorf("exit node = %+v", vs[0].Tailscale)
	}
	// error path: the switch snaps back and shows the hint
	r.ts.Fail("Disconnect", core.Errorf(core.KindPermission, "run: sudo tailscale set --operator=$USER", "tailscale: not operator"))
	r.ui(func() { test.Tap(v.switches["tailscale"]) })
	r.idle()
	r.ui(func() {
		if !v.switches["tailscale"].Checked {
			t.Error("switch should snap back on failure")
		}
		if !strings.Contains(v.actErr.Text, "operator") {
			t.Errorf("error = %q", v.actErr.Text)
		}
	})
	r.ts.Fail("Disconnect", nil)
	// import from file content
	r.ui(func() { v.importContent("office.conf", "[Interface]\nPrivateKey = k=\nAddress = 10.9.0.2/24\n") })
	r.idle()
	ps, _ := r.c.Profiles(context.Background())
	imported := false
	for _, p := range ps {
		if p.Type == core.ProfileWireGuard && p.Name == "office" {
			imported = true
		}
	}
	if !imported {
		t.Errorf("imported profile missing from %d profiles", len(ps))
	}
	r.ui(func() { v.importContent("notes.txt", "x") })
	r.ui(func() {
		if !strings.Contains(v.actErr.Text, ".ovpn") {
			t.Errorf("bad extension error = %q", v.actErr.Text)
		}
	})
}

func TestDisconnectedEventUpdatesHeader(t *testing.T) {
	r := newRig(t)
	r.ui(func() {
		if r.a.header.name.Text != fake.HomeSSID {
			t.Fatalf("header = %q", r.a.header.name.Text)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for r.d.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	r.nm.DisconnectAll()
	r.waitFor("header to show not connected", func() bool { return r.a.header.name.Text == "Not connected" })
	r.waitFor("overview event list", func() bool {
		r.a.eventsMu.Lock()
		defer r.a.eventsMu.Unlock()
		for _, e := range r.a.events {
			if e.Type == core.EventDisconnected {
				return true
			}
		}
		return false
	})
	r.idle()
	r.ui(func() {
		found := false
		for _, o := range r.overview().events.Objects {
			if l, ok := o.(*fyne.Container).Objects[0].(*widget.Label); ok && strings.Contains(l.Text, "Disconnected") {
				found = true
			}
		}
		if !found {
			t.Error("overview should list the disconnected event")
		}
	})
}

func TestSettingsToggleWritesConfig(t *testing.T) {
	r := newRig(t)
	s := r.settings()
	r.ui(func() {
		if s.checks["notify.degraded"].Checked {
			t.Fatal("degraded notifications default off")
		}
		if s.provider.Selected != "cloudflare" || s.interval.Text != "30s" {
			t.Errorf("settings = %q %q", s.provider.Selected, s.interval.Text)
		}
		if s.daemon.values[0].Text != "test-1" {
			t.Errorf("daemon version = %q", s.daemon.values[0].Text)
		}
		test.Tap(s.checks["notify.degraded"])
	})
	r.idle()
	cfg, err := r.c.Config(context.Background())
	if err != nil || !cfg.Notify.Degraded {
		t.Errorf("config = %+v %v", cfg.Notify, err)
	}
	r.ui(func() { s.setKey("monitor.interval", "2s", s.monErr) })
	r.idle()
	cfg, _ = r.c.Config(context.Background())
	if cfg.Monitor.Interval != 2*time.Second {
		t.Errorf("interval = %s", cfg.Monitor.Interval)
	}
	// invalid values surface inline
	r.ui(func() { s.setKey("monitor.interval", "soon", s.monErr) })
	r.idle()
	r.ui(func() {
		if !s.monErr.Visible() {
			t.Error("invalid interval should show an error")
		}
	})
	// test notification reaches the notifier
	r.ui(func() { test.Tap(s.testBtn) })
	r.idle()
	if evs := r.notif.Events(); len(evs) != 1 {
		t.Errorf("notified = %d", len(evs))
	}
	// install goes through the stub
	r.ui(func() { test.Tap(s.install) })
	r.idle()
	r.ui(func() {
		if s.instOut.Text != "stubbed install" {
			t.Errorf("install out = %q", s.instOut.Text)
		}
	})
}

func TestSpeedProgressUpdatesBar(t *testing.T) {
	r := newRig(t)
	s := r.speedView()
	r.ui(func() {
		if s.bar.Value != 0 || !s.resultBox.Hidden {
			t.Fatal("fresh speed view should be empty")
		}
		test.Tap(s.quickBtn)
	})
	r.idle()
	r.ui(func() {
		if s.bar.Value != 1 {
			t.Errorf("bar = %v", s.bar.Value)
		}
		if s.phase.Text != "Done" {
			t.Errorf("phase = %q", s.phase.Text)
		}
		if !strings.Contains(s.live.Text, "240.1") {
			t.Errorf("live = %q", s.live.Text)
		}
		if s.resultBox.Hidden || s.result.values[0].Text != "240.1 Mbit/s" {
			t.Errorf("result = %q hidden=%v", s.result.values[0].Text, s.resultBox.Hidden)
		}
		if s.running || s.runBtn.Disabled() {
			t.Error("buttons should be re-enabled")
		}
	})
	r.waitFor("history row", func() bool { return len(s.rows) == 1 })
	if r.speed.Runs() != 1 {
		t.Errorf("runs = %d", r.speed.Runs())
	}
	// progress steps are reflected as they stream: replay one on the UI thread
	r.ui(func() {
		s.progress(core.SpeedProgress{Phase: "download", Mbps: 55.5, Percent: 40})
		if s.bar.Value < 0.39 || s.bar.Value > 0.41 || s.phase.Text != "Downloading" || s.live.Text != "55.5 Mbit/s" {
			t.Errorf("progress render = %v %q %q", s.bar.Value, s.phase.Text, s.live.Text)
		}
	})
	// failure path
	r.speed.Err = core.Errorf(core.KindUnavailable, "check your connection", "speed: unreachable")
	r.ui(func() { test.Tap(s.runBtn) })
	r.idle()
	r.ui(func() {
		if !s.actErr.Visible() || !strings.Contains(s.actErr.Text, "check your connection") {
			t.Errorf("error = %q", s.actErr.Text)
		}
	})
}

func TestQualityDevicesAdvancedRender(t *testing.T) {
	r := newRig(t)
	r.mon.AddSample(core.Sample{Time: time.Now(), NetworkKey: "wifi:" + fake.HomeSSID, Anchor: "gateway", RTTms: 2.5})
	r.mon.AddSample(core.Sample{Time: time.Now(), NetworkKey: "wifi:" + fake.HomeSSID, Anchor: "gateway", RTTms: 3.5})
	r.a.views[SectionQuality].refresh()
	r.idle()
	q := r.quality()
	r.ui(func() {
		if len(q.rows) != 2 || q.rows[0][0] != "gateway" {
			t.Errorf("anchor rows = %v", q.rows)
		}
		if len(q.sparks["gateway"].values) != 2 {
			t.Errorf("sparkline values = %v", q.sparks["gateway"].values)
		}
		test.Tap(q.pause)
	})
	r.idle()
	r.waitFor("pause button to flip", func() bool { return q.pause.Text == "Resume" })
	ms, _ := r.c.Monitor(context.Background())
	if !ms.Paused {
		t.Error("monitor should be paused")
	}

	d := r.devices()
	r.ui(func() {
		if len(d.physical.Objects) != 2 {
			t.Errorf("physical rows = %d", len(d.physical.Objects))
		}
		if d.virtItem.Title != "Virtual (0)" {
			t.Errorf("virtual title = %q", d.virtItem.Title)
		}
	})

	adv := r.advanced()
	r.ui(func() {
		if len(adv.ports.rows) != 3 || len(adv.routes.rows) != 3 {
			t.Errorf("ports = %d routes = %d", len(adv.ports.rows), len(adv.routes.rows))
		}
		if len(adv.infraNodes[""]) != 1 {
			t.Errorf("infra roots = %v", adv.infraNodes[""])
		}
		adv.dnsName.SetText("example.com")
		adv.lookup()
	})
	r.idle()
	r.ui(func() {
		if adv.dnsOut.values[0].Text == "" || adv.dnsOut.values[0].Text == "-" {
			t.Errorf("dns answers = %q", adv.dnsOut.values[0].Text)
		}
		adv.loadPublicIP()
	})
	r.idle()
	r.ui(func() {
		if adv.pubIP.values[0].Text == "" || adv.pubIP.values[0].Text == "checking" {
			t.Errorf("public ip = %q", adv.pubIP.values[0].Text)
		}
	})
}

func TestNavigationAndTrayMenu(t *testing.T) {
	r := newRig(t)
	r.ui(func() {
		r.a.nav.Select(int(SectionSpeed))
		if r.a.current != SectionSpeed || r.a.content.Objects[0] != r.a.panes[SectionSpeed] {
			t.Errorf("current = %v", r.a.current)
		}
	})
	// the tray menu is built from the same state even when no host is present
	tr := &tray{a: r.a}
	st, _ := r.c.Status(context.Background())
	vpns, _ := r.c.VPNs(context.Background())
	m := tr.menu(trayState{connection: st.Primary.ProfileName, wifiOn: true, wifiHW: true, vpns: vpns, verdict: "ok"})
	var labels []string
	for _, it := range m.Items {
		if !it.IsSeparator {
			labels = append(labels, it.Label)
		}
	}
	want := []string{fake.HomeSSID, "Wi-Fi", "Tailscale  (Tailscale)", "wg-home  (WireGuard)", "office-ovpn  (OpenVPN)", "Quality: ok", "Open bnm", "Quit"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Errorf("menu = %v", labels)
	}
	if !m.Items[2].Checked {
		t.Error("wifi item should be checked")
	}
}

func TestUnitText(t *testing.T) {
	u := UnitText("/opt/bnm/bnmd")
	for _, want := range []string{"ExecStart=/opt/bnm/bnmd\n", "WantedBy=default.target", "Restart=on-failure"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if p := UserUnitPath(); p != "/x/systemd/user/bnmd.service" {
		t.Errorf("unit path = %s", p)
	}
}

func TestErrTextCarriesHint(t *testing.T) {
	err := &client.APIError{Status: 403, Code: core.KindPermission, Message: "nm: not allowed", Hint: "ask your admin"}
	if got := errText(err); got != "nm: not allowed\nask your admin" {
		t.Errorf("errText = %q", got)
	}
	if got := errText(errors.New("plain")); got != "plain" {
		t.Errorf("plain = %q", got)
	}
}

func TestHelpers(t *testing.T) {
	if fmtMs(-1) != "lost" || fmtMs(12.34) != "12.3 ms" || fmtMs(250) != "250 ms" {
		t.Error("fmtMs")
	}
	if fmtBytes(44_000_000) != "42.0 MiB" {
		t.Errorf("fmtBytes = %s", fmtBytes(44_000_000))
	}
	if got := splitList("1.1.1.1, 9.9.9.9 8.8.8.8"); len(got) != 3 {
		t.Errorf("splitList = %v", got)
	}
	hs := headerState{}
	if hs.connectionName() != "Not connected" || hs.verdict() != "unknown" {
		t.Error("empty header state")
	}
}

// containsObject reports whether obj is somewhere inside c.
func containsObject(c *fyne.Container, obj fyne.CanvasObject) bool {
	for _, o := range c.Objects {
		if o == obj {
			return true
		}
		if inner, ok := o.(*fyne.Container); ok && containsObject(inner, obj) {
			return true
		}
	}
	return false
}
