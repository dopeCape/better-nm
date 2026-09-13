package tui

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
	"github.com/dopeCape/better-nm/internal/fake"
)

// rig is a real daemon + API server over a temp socket, backed by fakes.
type rig struct {
	t     *testing.T
	c     *client.Client
	nm    *fake.NM
	ts    *fake.VPNAdapter
	mon   *fake.Monitor
	store *fake.Store
	speed *fake.SpeedTester
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, nm: fake.NewNM(), ts: fake.NewTailscale(), mon: fake.NewMonitor(), store: fake.NewStore(), speed: fake.NewSpeedTester()}
	dir, err := os.MkdirTemp("", "bnmtui") // short: Unix socket paths are capped
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "bnmd.sock")
	t.Setenv("XDG_STATE_HOME", dir)
	d, err := daemon.New(daemon.Options{
		NM:                r.nm,
		VPN:               fake.NewVPNRegistry(r.ts, fake.NewWireGuard(), fake.NewNMVPN()),
		Monitor:           r.mon,
		Store:             r.store,
		Speed:             r.speed,
		Notifier:          fake.NewNotifier(),
		Diag:              fake.NewDiag(),
		WireGuard:         &fake.WireGuardImporter{NM: r.nm},
		Config:            config.Default(),
		ConfigPath:        filepath.Join(dir, "config.toml"),
		Logger:            slog.New(slog.DiscardHandler),
		Version:           "test-1",
		Debounce:          30 * time.Millisecond,
		ConnectivityGrace: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
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
	t.Cleanup(func() {
		cancel()
		for _, ch := range []chan error{runDone, serveDone} {
			select {
			case err := <-ch:
				if err != nil {
					t.Errorf("shutdown: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("shutdown timed out")
			}
		}
		sock.Close()
	})
	c, err := client.New(client.WithSocket(socket), client.WithAutoStart(false))
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	t.Cleanup(func() { c.Close() })
	return r
}

// harness drives the model in-process: every Cmd runs synchronously and its
// message is fed back, except the tickers (spinner, cursor blink) and the
// stream wait, which the test pumps by hand.
type harness struct {
	t      *testing.T
	r      *rig
	m      *Model
	stream <-chan client.StreamItem
	quit   bool
	cancel context.CancelFunc
}

func newHarness(t *testing.T, r *rig, w, h int) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cursorMode = cursor.CursorStatic
	m := New(ctx, r.c)
	m.flashTTL = time.Millisecond
	hz := &harness{t: t, r: r, m: m, cancel: cancel}
	hz.feed(tea.WindowSizeMsg{Width: w, Height: h})
	hz.run(m.Init())
	if hz.stream == nil {
		t.Fatal("Init did not open the stream")
	}
	return hz
}

func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	h.feed(cmd())
}

func (h *harness) feed(msg tea.Msg) {
	switch v := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, c := range v {
			h.run(c)
		}
		return
	case tea.QuitMsg:
		h.quit = true
		return
	case flashExpireMsg: // keep flashes visible for assertions
		return
	case streamOpenMsg:
		h.stream = v.ch
		_, _ = h.m.Update(v) // do not run the blocking wait
		return
	}
	if pkg := reflect.TypeOf(msg).PkgPath(); strings.HasSuffix(pkg, "/spinner") || strings.HasSuffix(pkg, "/cursor") {
		return
	}
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

func (h *harness) key(keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEscape}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		h.feed(msg)
	}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.feed(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// pump feeds live stream items into the model until pred is satisfied by an
// item (or the deadline passes), then fires the debounce reloads.
func (h *harness) pump(timeout time.Duration, pred func(client.StreamItem) bool) bool {
	h.t.Helper()
	deadline := time.After(timeout)
	hit := false
	for !hit {
		select {
		case item, ok := <-h.stream:
			if !ok {
				h.t.Fatal("stream closed")
			}
			h.run(h.m.onStreamItemForTest(item))
			if pred(item) {
				hit = true
			}
		case <-deadline:
			return false
		}
	}
	// drain what is already queued, then flush the debounce
	for {
		select {
		case item := <-h.stream:
			h.run(h.m.onStreamItemForTest(item))
			continue
		default:
		}
		break
	}
	h.feed(debounceMsg{})
	return true
}

// onStreamItemForTest folds the item in without arming the real debounce timer.
func (m *Model) onStreamItemForTest(item client.StreamItem) tea.Cmd {
	m.onStreamItem(item)
	m.st.debouncin = true // pump flushes with an explicit debounceMsg
	return nil
}

func (h *harness) view() string { return h.m.View() }

func mustContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("view lacks %q:\n%s", sub, s)
		}
	}
}

func mustNotContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Errorf("view should not contain %q:\n%s", sub, s)
		}
	}
}

func lines(s string) []string { return strings.Split(s, "\n") }

func TestInitialRenderShowsHomeNetActive(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	v := h.view()
	mustContain(t, v, "HomeNet", "● active", "192.168.1.42", "● full", "bnmd test-1", "vpn 0/3", "monitor")
	// active first, then by signal: HomeNet, CoffeeShop, Neighbour5G, Office
	order := []string{"HomeNet", "CoffeeShop", "Neighbour5G", "Office"}
	last := -1
	for _, ssid := range order {
		i := strings.Index(v, ssid)
		if i < last {
			t.Errorf("%s out of order in\n%s", ssid, v)
		}
		last = i
	}
	mustContain(t, v, "▂▄▆█", "82%", "5 GHz ch36", "wpa2", "open", "known")
	if h.m.tab != tabWifi {
		t.Error("default tab must be Wi-Fi")
	}
}

func TestTabSwitching(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.key("2")
	mustContain(t, h.view(), "eth0", "wlan0", "ethernet", "disconnected")
	h.key("3")
	mustContain(t, h.view(), "Tailscale", "wg-home", "office-ovpn", "OpenVPN")
	h.key("4")
	mustContain(t, h.view(), "Monitor")
	h.key("5")
	mustContain(t, h.view(), "Speed", "history")
	h.key("6")
	mustContain(t, h.view(), "Diag", "[LAN]", "router.lan")
	h.key("tab")
	if h.m.tab != tabWifi {
		t.Errorf("tab wraps to Wi-Fi, got %d", h.m.tab)
	}
	h.key("shift+tab")
	if h.m.tab != tabDiag {
		t.Errorf("shift+tab wraps to Diag, got %d", h.m.tab)
	}
	h.key("1")
	mustContain(t, h.view(), "HomeNet")
}

func TestWifiConnectFlowWithPassword(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	// cursor: HomeNet(0) CoffeeShop(1) Neighbour5G(2) Office(3)
	h.key("down", "down")
	if n := h.m.wifi.selected(); n == nil || n.SSID != fake.NeighbourSSD {
		t.Fatalf("selected = %+v", n)
	}
	h.key("enter")
	if h.m.wifi.prompt == nil {
		t.Fatal("expected a password prompt for an unknown WPA3 network")
	}
	mustContain(t, h.view(), "join Neighbour5G", "password:")
	h.typeText("hunter22")
	mustNotContain(t, h.view(), "hunter22") // masked
	h.key("enter")
	if h.m.wifi.prompt != nil {
		t.Fatal("prompt should close on enter")
	}
	// the action ran synchronously through the harness: the fake now has it active
	active, _ := r.nm.ActiveConnections(context.Background())
	found := false
	for _, a := range active {
		if a.ProfileName == fake.NeighbourSSD {
			found = true
		}
	}
	if !found {
		t.Fatalf("Neighbour5G not activated: %+v", active)
	}
	h.run(h.m.l.wifi())
	v := h.view()
	if !strings.Contains(v, "Neighbour5G") || h.m.wifi.connecting != "" {
		t.Errorf("connecting=%q view:\n%s", h.m.wifi.connecting, v)
	}
	if !h.m.wifi.visible()[0].Active || h.m.wifi.visible()[0].SSID != fake.NeighbourSSD {
		t.Errorf("Neighbour5G should now be first and active: %+v", h.m.wifi.visible()[0])
	}

	// an open network connects without a prompt
	r.nm.Fail("ConnectWifi", core.Errorf(core.KindPermission, "run: bnm auth", "nm: not authorised"))
	for i, n := range h.m.wifi.visible() {
		if n.SSID == fake.CafeSSID {
			h.m.wifi.cursor = i
		}
	}
	h.key("enter")
	if h.m.wifi.prompt != nil {
		t.Fatal("open network must not prompt")
	}
	v = h.view()
	mustContain(t, v, "not authorised", "run: bnm auth") // error with its hint
	r.nm.Fail("ConnectWifi", nil)
	h.m.flash = flash{}

	// filter
	h.key("/")
	h.typeText("office")
	h.key("enter")
	v = h.view()
	mustContain(t, v, "Office")
	mustNotContain(t, v, "CoffeeShop")
	h.key("esc")
	mustContain(t, h.view(), "CoffeeShop")
}

func TestWifiForgetConfirmAndDisconnect(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.key("f")
	mustContain(t, h.view(), "forget HomeNet? (y/n)")
	h.key("n")
	if ps, _ := r.nm.Profiles(context.Background()); len(ps) != 3 {
		t.Errorf("n must keep the profile: %d", len(ps))
	}
	h.key("f", "y")
	if ps, _ := r.nm.Profiles(context.Background()); len(ps) != 2 {
		t.Errorf("y must forget the profile: %d", len(ps))
	}
	h.run(h.m.l.wifi())
	h.key("d")
	if active, _ := r.nm.ActiveConnections(context.Background()); len(active) != 0 {
		t.Errorf("d must disconnect: %+v", active)
	}
}

func TestVPNToggleSendsRequest(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.key("3")
	if v := h.m.vpn.selected(); v == nil || v.ID != "tailscale" {
		t.Fatalf("first VPN row = %+v", v)
	}
	h.key("enter")
	vs, _ := r.ts.List(context.Background())
	if vs[0].State != core.VPNConnected {
		t.Fatalf("enter must connect: %s", vs[0].State)
	}
	mustContain(t, h.view(), "connected", "vpn 1/3")
	h.key("enter")
	vs, _ = r.ts.List(context.Background())
	if vs[0].State != core.VPNDisconnected {
		t.Fatalf("enter again must disconnect: %s", vs[0].State)
	}
	// exit node picker
	h.key("e")
	if h.m.vpn.picker == nil || h.m.over != overlayNone {
		t.Fatal("e on Tailscale opens the exit-node picker, not the event log")
	}
	mustContain(t, h.view(), "exit node", "homeserver")
	h.key("enter")
	vs, _ = r.ts.List(context.Background())
	if !vs[0].Tailscale.ExitNodeOn || vs[0].Tailscale.ExitNodeName != "homeserver" {
		t.Errorf("exit node not set: %+v", vs[0].Tailscale)
	}
	// login shows the URL
	h.key("l")
	mustContain(t, h.view(), "https://login.tailscale.com/a/fake")
	// e on a non-Tailscale row opens the events overlay; E always does
	h.key("down", "e")
	if h.m.over != overlayEvents {
		t.Error("e on a non-Tailscale row opens the events log")
	}
	h.key("esc", "E")
	if h.m.over != overlayEvents {
		t.Error("E opens the events log")
	}
	h.key("esc")
	// add prompts for a file and rejects the wrong extension
	h.key("a")
	h.typeText("/tmp/x.txt")
	h.key("enter")
	mustContain(t, h.view(), "expected a .conf")
}

func TestDisconnectedEventUpdatesStatusBarAndFooter(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	mustContain(t, h.view(), "HomeNet 192.168.1.42")
	r.nm.DisconnectAll()
	ok := h.pump(5*time.Second, func(it client.StreamItem) bool {
		return it.Event != nil && it.Event.Type == core.EventDisconnected
	})
	if !ok {
		t.Fatal("no disconnected event arrived")
	}
	v := h.view()
	mustContain(t, v, "not connected", "Disconnected from HomeNet")
	mustNotContain(t, v, "HomeNet 192.168.1.42", "● active")
	if len(h.m.events) == 0 || h.m.events[len(h.m.events)-1].Type != core.EventDisconnected {
		t.Errorf("event log: %+v", h.m.events)
	}
	h.key("e")
	mustContain(t, h.view(), "events (", "Disconnected from HomeNet")
}

func TestMonitorTabRendersAnchors(t *testing.T) {
	r := newRig(t)
	now := time.Now()
	r.mon.SetStatus(core.MonitorStatus{
		NetworkKey: "wifi:HomeNet", State: core.BaselineDegraded, Interval: 30 * time.Second, LastSample: now,
		Anchors: []core.Baseline{
			{NetworkKey: "wifi:HomeNet", Anchor: "gateway", State: core.BaselineOK, SampleCount: 50, BaselineRTT: 2.1, CurrentRTT: 2.4, CurrentDNS: 9.5},
			{NetworkKey: "wifi:HomeNet", Anchor: "1.1.1.1", State: core.BaselineDegraded, SampleCount: 50, BaselineRTT: 12, CurrentRTT: 88, CurrentLoss: 0.1, Since: now.Add(-3 * time.Minute)},
			{NetworkKey: "wifi:HomeNet", Anchor: "8.8.8.8", State: core.BaselineLearning, SampleCount: 12},
		},
	})
	for i := 0; i < 70; i++ {
		rtt := float64(2 + i%5)
		if i == 30 {
			rtt = -1
		}
		r.mon.AddSample(core.Sample{Time: now.Add(time.Duration(i-70) * 30 * time.Second), NetworkKey: "wifi:HomeNet", Anchor: "gateway", RTTms: rtt, DNSms: 9.5, Method: "icmp"})
		r.mon.AddSample(core.Sample{Time: now.Add(time.Duration(i-70) * 30 * time.Second), NetworkKey: "wifi:HomeNet", Anchor: "1.1.1.1", RTTms: 10 + float64(i), Method: "icmp"})
	}
	h := newHarness(t, r, 100, 30)
	h.key("4")
	v := h.view()
	mustContain(t, v, "degraded since", "gateway", "1.1.1.1", "8.8.8.8", "learning 12/40", "2.4 ms", "/ 2.1 ms", "88 ms", "10%", "dns 9.5 ms", "last 60 samples", "×")
	for _, g := range []string{"▁", "█"} {
		if !strings.Contains(v, g) {
			t.Errorf("sparkline glyph %q missing:\n%s", g, v)
		}
	}
	mustContain(t, v, "monitor degraded")
	// pause / resume round-trip
	h.key("p")
	if !r.mon.Status().Paused {
		t.Error("p should pause")
	}
	mustContain(t, h.view(), "paused")
	h.key("p")
	if r.mon.Status().Paused {
		t.Error("p again should resume")
	}
	h.key("R")
	if got := r.mon.Resets(); len(got) != 1 {
		t.Errorf("R should reset the baseline once: %v", got)
	}
}

func TestSpeedProgressAdvancesBar(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.key("5")
	mustContain(t, h.view(), "no tests yet")
	// progress messages move the bars, phase by phase
	h.m.speed.running = true
	h.feed(speedProgressMsg{core.SpeedProgress{Phase: "download", Percent: 50, Mbps: 120}})
	v := h.view()
	mustContain(t, v, "120 Mbps")
	rows := lines(v)
	var latency, download string
	for _, l := range rows {
		if strings.Contains(l, "latency") && strings.Contains(l, "█") {
			latency = l
		}
		if strings.Contains(l, "download") && strings.Contains(l, "█") {
			download = l
		}
	}
	if strings.Count(latency, "█") <= strings.Count(download, "█") || strings.Count(download, "░") == 0 {
		t.Errorf("latency should be full, download half:\n%s\n%s", latency, download)
	}
	h.feed(speedProgressMsg{core.SpeedProgress{Phase: "download", Percent: 100, Mbps: 240}})
	if strings.Count(h.view(), "░") >= strings.Count(v, "░") {
		t.Error("more progress should fill more of the bar")
	}
	h.m.speed.running = false
	// a real run against the fake tester goes through the whole flow
	h.key("s")
	if h.m.speed.running || h.m.speed.last == nil {
		t.Fatalf("running=%v last=%+v err=%v", h.m.speed.running, h.m.speed.last, h.m.speed.lastErr)
	}
	v = h.view()
	mustContain(t, v, "240 Mbps", "18.3 Mbps", "12 ms (jitter 1.1 ms)", "cloudflare")
	if r.speed.Runs() != 1 {
		t.Errorf("runs = %d", r.speed.Runs())
	}
	h.key("q") // quick test, not quit
	if h.quit {
		t.Fatal("q on the Speed tab must not quit")
	}
	if r.speed.Runs() != 2 {
		t.Errorf("q should run a quick test: runs = %d", r.speed.Runs())
	}
	mustContain(t, h.view(), "cloudflare quick")
}

func TestResizeAndSmallTerminal(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 120, 40)
	h.feed(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, tabKey := range []string{"1", "2", "3", "4", "5", "6"} {
		h.key(tabKey)
		v := h.view()
		ls := lines(v)
		if len(ls) != 24 {
			t.Errorf("tab %s: %d lines at 80x24", tabKey, len(ls))
		}
		for i, l := range ls {
			if w := lipgloss.Width(l); w > 80 {
				t.Errorf("tab %s line %d is %d wide: %q", tabKey, i, w, l)
			}
		}
	}
	h.key("1")
	mustContain(t, h.view(), "HomeNet", "1 Wi-Fi")
	h.feed(tea.WindowSizeMsg{Width: 60, Height: 20})
	v := h.view()
	if len(lines(v)) != 20 {
		t.Errorf("%d lines at 60x20", len(lines(v)))
	}
	mustContain(t, v, "HomeNet")
	mustNotContain(t, v, "1 Wi-Fi") // the rail collapses to numbers
	h.feed(tea.WindowSizeMsg{Width: 30, Height: 5})
	mustContain(t, h.view(), "needs at least")
}

func TestHelpOverlayAndQuit(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.key("?")
	mustContain(t, h.view(), "bnm keys", "wifi on/off", "exit node", "reset baseline")
	h.key("esc")
	mustNotContain(t, h.view(), "bnm keys")
	h.key("q")
	if !h.quit {
		t.Error("q quits")
	}
}

func TestDevicesTabWiredToggleAndVirtual(t *testing.T) {
	r := newRig(t)
	r.nm.Emit(core.Change{Kind: core.ChangeDevices})
	h := newHarness(t, r, 100, 30)
	h.key("2", "down") // eth0
	if row := h.m.devices.rows()[h.m.devices.cursor]; row.dev == nil || row.dev.Name != fake.WiredDevice {
		t.Fatalf("cursor on %+v", row)
	}
	h.key("enter")
	active, _ := r.nm.ActiveConnections(context.Background())
	found := false
	for _, a := range active {
		if a.ProfileUUID == fake.WiredUUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("enter on eth0 should activate the wired profile: %+v", active)
	}
	mustContain(t, h.view(), "Wired connection 1")
	h.key("enter")
	active, _ = r.nm.ActiveConnections(context.Background())
	for _, a := range active {
		if a.ProfileUUID == fake.WiredUUID {
			t.Fatalf("enter again should bring eth0 down: %+v", active)
		}
	}
	mustNotContain(t, h.view(), "Virtual (")
}

func TestDiagSubViews(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.key("6")
	mustContain(t, h.view(), "[LAN]", "192.168.1.1", "router.lan", "gateway")
	h.key("s")
	mustContain(t, h.view(), "192.168.1.77") // the sweep found one more
	h.key("]")
	mustContain(t, h.view(), "[Ports]", "sshd (811)", "5353")
	h.key("]")
	mustContain(t, h.view(), "[Routes]", "default", "192.168.1.1", "dhcp")
	h.key("]")
	mustContain(t, h.view(), "[Infra]", "docker0", "veth1a2b3c", "└─")
	h.key("]")
	mustContain(t, h.view(), "[Public IP]", "203.0.113.7", "FRA")
	h.key("]")
	mustContain(t, h.view(), "[DNS]", "lookup:")
	h.typeText("example.com")
	h.key("enter")
	mustContain(t, h.view(), "93.184.216.34", "example.com")
	h.key("esc", "[")
	mustContain(t, h.view(), "[Public IP]")
}

func TestStreamReconnectShowsInStatusBar(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	mustContain(t, h.view(), "bnmd test-1")
	_, cmd := h.m.Update(streamClosedMsg{})
	if cmd == nil {
		t.Fatal("a closed stream schedules a retry")
	}
	mustContain(t, h.view(), "daemon: reconnecting…")
	if h.m.st.backoff != backoffMin {
		t.Errorf("first backoff = %v", h.m.st.backoff)
	}
	h.m.Update(streamErrMsg{err: context.DeadlineExceeded})
	if h.m.st.backoff != 2*backoffMin {
		t.Errorf("second backoff = %v", h.m.st.backoff)
	}
	// a fresh open marks it up again and reloads
	h.run(h.m.l.openStream())
	mustContain(t, h.view(), "bnmd test-1")
	mustNotContain(t, h.view(), "reconnecting")
}

// TestProgramLiveStream runs the whole program under teatest: the real stream
// goroutine, debounce timer and renderer.
func TestProgramLiveStream(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, r.c)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("HomeNet 192.168.1.42")) && bytes.Contains(b, []byte("● active"))
	}, teatest.WithDuration(5*time.Second))
	r.nm.DisconnectAll()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("not connected")) && bytes.Contains(b, []byte("Disconnected from HomeNet"))
	}, teatest.WithDuration(5*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}
