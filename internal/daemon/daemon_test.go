package daemon

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/fake"
)

// world bundles a daemon with its fakes.
type world struct {
	d        *Daemon
	nm       *fake.NM
	ts       *fake.VPNAdapter
	mon      *fake.Monitor
	store    *fake.Store
	notifier *fake.Notifier
	speed    *fake.SpeedTester
	cancel   context.CancelFunc
	done     chan error
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{
		nm:       fake.NewNM(),
		ts:       fake.NewTailscale(),
		mon:      fake.NewMonitor(),
		store:    fake.NewStore(),
		notifier: fake.NewNotifier(),
		speed:    fake.NewSpeedTester(),
	}
	d, err := New(Options{
		NM:                w.nm,
		VPN:               fake.NewVPNRegistry(w.ts, fake.NewWireGuard()),
		Monitor:           w.mon,
		Store:             w.store,
		Speed:             w.speed,
		Notifier:          w.notifier,
		Diag:              fake.NewDiag(),
		WireGuard:         &fake.WireGuardImporter{NM: w.nm},
		Config:            config.Default(),
		ConfigPath:        filepath.Join(t.TempDir(), "config.toml"),
		Logger:            slog.New(slog.DiscardHandler),
		Version:           "test",
		Debounce:          50 * time.Millisecond,
		ConnectivityGrace: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.d = d
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan error, 1)
	go func() { w.done <- d.Run(ctx) }()
	// wait for the initial read
	deadline := time.Now().Add(2 * time.Second)
	for d.Snapshot().Version == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-w.done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("Run did not stop")
		}
	})
	return w
}

// waitEvent blocks until an event of type typ arrives on ch.
func waitEvent(t *testing.T, ch <-chan StreamItem, typ core.EventType, within time.Duration) core.Event {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case it, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed while waiting for %s", typ)
			}
			if it.Event != nil && it.Event.Type == typ {
				return *it.Event
			}
		case <-deadline:
			t.Fatalf("no %s event within %v", typ, within)
		}
	}
}

func waitChange(t *testing.T, ch <-chan StreamItem, kind core.ChangeKind, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case it, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed while waiting for change %s", kind)
			}
			if it.Change != nil && it.Change.Kind == kind {
				return
			}
		case <-deadline:
			t.Fatalf("no %s change within %v", kind, within)
		}
	}
}

func noEvent(t *testing.T, ch <-chan StreamItem, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case it := <-ch:
			if it.Event != nil {
				t.Fatalf("unexpected event %s (%s)", it.Event.Type, it.Event.Title)
			}
		case <-deadline:
			return
		}
	}
}

func TestNewRequiresNMAndStore(t *testing.T) {
	if _, err := New(Options{Store: fake.NewStore()}); err == nil {
		t.Error("want error without NM")
	}
	if _, err := New(Options{NM: fake.NewNM()}); err == nil {
		t.Error("want error without Store")
	}
}

func TestInitialSnapshotAndMonitorNetwork(t *testing.T) {
	w := newWorld(t)
	s := w.d.Snapshot()
	if s.Status.Primary == nil || s.Status.NetworkKey != "wifi:"+fake.HomeSSID || len(s.Devices) != 2 || len(s.Wifi) != 4 || len(s.Profiles) != 3 || len(s.VPNs) != 2 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Monitor.NetworkKey != "wifi:"+fake.HomeSSID {
		t.Errorf("monitor status not refreshed: %+v", s.Monitor)
	}
	nets := w.mon.Networks()
	if len(nets) != 1 || nets[0].Key != "wifi:"+fake.HomeSSID || nets[0].Gateway != "192.168.1.1" {
		t.Errorf("SetNetwork calls = %+v", nets)
	}
	if !w.mon.Running() {
		t.Error("monitor not running")
	}
	if len(w.notifier.Events()) != 0 {
		t.Errorf("startup must not notify: %+v", w.notifier.Events())
	}
	if w.d.Uptime() < 0 || w.d.Version() != "test" {
		t.Error("uptime/version")
	}
}

func TestDisconnectFlow(t *testing.T) {
	w := newWorld(t)
	ch, cancel := w.d.Subscribe()
	defer cancel()
	if w.d.Subscribers() != 1 {
		t.Errorf("subscribers = %d", w.d.Subscribers())
	}
	v0 := w.d.Snapshot().Version
	w.nm.DisconnectAll()
	waitChange(t, ch, core.ChangeActive, time.Second)
	e := waitEvent(t, ch, core.EventDisconnected, time.Second)
	if e.Title != "Disconnected from "+fake.HomeSSID {
		t.Errorf("event = %+v", e)
	}
	if w.d.Snapshot().Version <= v0 || w.d.Status().Primary != nil {
		t.Errorf("snapshot not updated: %+v", w.d.Status())
	}
	// stored, notified, and monitor told
	evs, _ := w.store.Events(context.Background(), 0)
	if len(evs) != 1 || evs[0].Type != core.EventDisconnected {
		t.Errorf("stored = %+v", evs)
	}
	select {
	case n := <-w.notifier.C():
		if n.Type != core.EventDisconnected {
			t.Errorf("notified %s", n.Type)
		}
	case <-time.After(time.Second):
		t.Error("notifier not called")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if nets := w.mon.Networks(); len(nets) >= 2 && nets[len(nets)-1].Key == "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if nets := w.mon.Networks(); nets[len(nets)-1].Key != "" {
		t.Errorf("monitor not told about disconnect: %+v", nets)
	}
}

func TestRoamIsSilent(t *testing.T) {
	w := newWorld(t)
	ch, cancel := w.d.Subscribe()
	defer cancel()
	w.nm.Roam()
	noEvent(t, ch, 200*time.Millisecond)
	if nets := w.mon.Networks(); len(nets) != 1 {
		t.Errorf("roam must not reset the monitor network: %+v", nets)
	}
}

func TestConnectViaAPIAndPortal(t *testing.T) {
	w := newWorld(t)
	ch, cancel := w.d.Subscribe()
	defer cancel()
	ctx := context.Background()
	if err := w.d.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: fake.CafeSSID}); err != nil {
		t.Fatal(err)
	}
	// synchronous refresh: the next read already sees the new primary
	if st := w.d.Status(); st.Primary == nil || st.Primary.ProfileName != fake.CafeSSID {
		t.Fatalf("status after connect = %+v", st)
	}
	e := waitEvent(t, ch, core.EventConnected, time.Second)
	if e.Title != "Connected to "+fake.CafeSSID {
		t.Errorf("event = %+v", e)
	}
	w.nm.SetConnectivity(core.ConnPortal)
	e = waitEvent(t, ch, core.EventNoInternet, time.Second)
	if !strings.Contains(e.Body, "login page") {
		t.Errorf("portal body = %s", e.Body)
	}
	w.nm.SetConnectivity(core.ConnFull)
	waitEvent(t, ch, core.EventInternetRestored, time.Second)
	if err := w.d.ConnectWifi(ctx, core.ConnectWifiRequest{}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("empty ssid: %v", err)
	}
}

func TestVPNFlow(t *testing.T) {
	w := newWorld(t)
	ch, cancel := w.d.Subscribe()
	defer cancel()
	ctx := context.Background()
	if err := w.d.ConnectVPN(ctx, "tailscale"); err != nil {
		t.Fatal(err)
	}
	e := waitEvent(t, ch, core.EventVPNUp, time.Second)
	if e.Title != "Tailscale connected" {
		t.Errorf("event = %+v", e)
	}
	// an external state flip (tailscale CLI) is seen through Watch
	w.ts.SetState("tailscale", core.VPNDisconnected)
	waitEvent(t, ch, core.EventVPNDown, time.Second)
	if err := w.d.ConnectVPN(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown vpn: %v", err)
	}
	if err := w.d.SetExitNode(ctx, "homeserver", true); err != nil {
		t.Fatal(err)
	}
	for _, v := range w.d.VPNs() {
		if v.ID == "tailscale" && (!v.Tailscale.ExitNodeOn || v.Tailscale.ExitNodeName != "homeserver") {
			t.Errorf("exit node not applied: %+v", v.Tailscale)
		}
	}
	url, err := w.d.TailscaleLogin(ctx)
	if err != nil || url == "" {
		t.Errorf("login: %q %v", url, err)
	}
	if err := w.d.UseExitNode(ctx, false); err != nil {
		t.Error(err)
	}
	if err := w.d.SetAcceptDNS(ctx, false); err != nil {
		t.Error(err)
	}
	if err := w.d.TailscaleLogout(ctx); err != nil {
		t.Error(err)
	}
}

func TestVPNUnsupportedWithoutRegistry(t *testing.T) {
	d, err := New(Options{NM: fake.NewNM(), Store: fake.NewStore(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.ConnectVPN(ctx, "x"); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("vpn: %v", err)
	}
	if _, err := d.TailscaleLogin(ctx); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("tailscale: %v", err)
	}
	if err := d.PauseMonitor(); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("monitor: %v", err)
	}
	if _, err := d.RunSpeed(ctx, core.SpeedOptions{}, nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("speed: %v", err)
	}
	if _, err := d.Diag().Routes(ctx); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("diag: %v", err)
	}
	if err := d.NotifyTest(ctx); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("notify: %v", err)
	}
	if _, err := d.ImportVPN(ctx, ImportVPNRequest{Kind: "wireguard", Content: "x", Name: "n"}); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("wg import: %v", err)
	}
	// samples fall back to the store without a monitor
	if _, err := d.Samples(ctx, "", "", 0); err != nil {
		t.Errorf("samples: %v", err)
	}
}

func TestNoTailscaleAdapter(t *testing.T) {
	d, err := New(Options{NM: fake.NewNM(), Store: fake.NewStore(), VPN: fake.NewVPNRegistry(fake.NewWireGuard()), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetExitNode(context.Background(), "x", false); !errors.Is(err, core.ErrUnsupported) || core.HintOf(err) == "" {
		t.Errorf("want unsupported with hint, got %v", err)
	}
}

func TestImportVPN(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	tests := []struct {
		name string
		req  ImportVPNRequest
		kind core.ErrorKind
	}{
		{"nothing", ImportVPNRequest{Kind: "wireguard"}, core.KindInvalid},
		{"both", ImportVPNRequest{Kind: "wireguard", Path: "/x", Content: "y"}, core.KindInvalid},
		{"bad kind", ImportVPNRequest{Kind: "ipsec", Content: "y"}, core.KindInvalid},
		{"wg no name", ImportVPNRequest{Kind: "wireguard", Content: "PrivateKey = a="}, core.KindInvalid},
		{"wg missing file", ImportVPNRequest{Kind: "wireguard", Path: filepath.Join(t.TempDir(), "none.conf")}, core.KindNotFound},
		{"ovpn missing file", ImportVPNRequest{Kind: "openvpn", Path: filepath.Join(t.TempDir(), "none.ovpn")}, core.KindNotFound},
		{"wg ok", ImportVPNRequest{Kind: "wireguard", Name: "wg-test", Content: "[Interface]\nPrivateKey = abc=\nAddress = 10.0.0.2/24\n"}, ""},
		{"ovpn ok", ImportVPNRequest{Kind: "openvpn", Name: "office", Content: "client\nremote vpn.example 1194\n"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "ovpn ok" {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
			}
			res, err := w.d.ImportVPN(ctx, tt.req)
			if tt.kind != "" {
				if core.KindOf(err) != tt.kind {
					t.Fatalf("err = %v (kind %s), want %s", err, core.KindOf(err), tt.kind)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.UUID == "" || res.ID != res.UUID {
				t.Errorf("result = %+v", res)
			}
			if _, err := w.d.Profile(res.UUID); err != nil {
				t.Errorf("profile not in snapshot: %v", err)
			}
		})
	}
	// a path-based wireguard import
	p := filepath.Join(t.TempDir(), "wg-file.conf")
	if err := writeFile(p, "[Interface]\nPrivateKey = abc=\n"); err != nil {
		t.Fatal(err)
	}
	res, err := w.d.ImportVPN(ctx, ImportVPNRequest{Kind: "wg", Path: p})
	if err != nil || res.Name != "wg-file" {
		t.Errorf("path import: %+v %v", res, err)
	}
}

func TestProfilesAndWifiOps(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.d.Wifi("nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("wifi unknown device: %v", err)
	}
	if _, err := w.d.Wifi(fake.WiredDevice); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("wifi on wired: %v", err)
	}
	nets, err := w.d.Wifi(fake.WifiDevice)
	if err != nil || len(nets) != 4 {
		t.Errorf("wifi = %d %v", len(nets), err)
	}
	if err := w.d.ScanWifi(ctx, ""); err != nil {
		t.Error(err)
	}
	if err := w.d.SetAutoconnect(ctx, fake.HomeUUID, false); err != nil {
		t.Error(err)
	}
	if p, _ := w.d.Profile(fake.HomeUUID); p.Autoconnect {
		t.Error("autoconnect not applied to snapshot")
	}
	if err := w.d.UpdateIPConfig(ctx, fake.HomeUUID, nil, nil); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("empty ip update: %v", err)
	}
	if err := w.d.UpdateIPConfig(ctx, fake.HomeUUID, &core.IPConfig{Method: core.IPManual, Addresses: []string{"10.0.0.2/24"}}, nil); err != nil {
		t.Error(err)
	}
	if err := w.d.Activate(ctx, fake.WiredUUID, ""); err != nil {
		t.Error(err)
	}
	if len(w.d.Active()) != 2 {
		t.Errorf("active = %d", len(w.d.Active()))
	}
	if err := w.d.Deactivate(ctx, fake.WiredUUID); err != nil {
		t.Error(err)
	}
	if err := w.d.DeleteProfile(ctx, fake.OfficeUUID); err != nil {
		t.Error(err)
	}
	if len(w.d.Profiles()) != 2 {
		t.Errorf("profiles = %d", len(w.d.Profiles()))
	}
	if err := w.d.Forget(ctx, ""); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("forget empty: %v", err)
	}
	if err := w.d.DisconnectDevice(ctx, ""); err != nil {
		t.Error(err)
	}
	if w.d.Status().Primary != nil {
		t.Error("disconnect default device should drop the primary")
	}
	if err := w.d.SetWifiEnabled(ctx, false); err != nil {
		t.Error(err)
	}
	if w.d.Status().WifiEnabled {
		t.Error("wifi enabled flag not refreshed")
	}
	if err := w.d.DisconnectDevice(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("disconnect unknown: %v", err)
	}
	if len(w.d.Devices()) != 2 {
		t.Error("devices")
	}
}

func TestMonitorOps(t *testing.T) {
	w := newWorld(t)
	ch, cancel := w.d.Subscribe()
	defer cancel()
	ctx := context.Background()
	if err := w.d.PauseMonitor(); err != nil || !w.d.MonitorStatus().Paused {
		t.Errorf("pause: %v %+v", err, w.d.MonitorStatus())
	}
	if err := w.d.ResumeMonitor(); err != nil || w.d.MonitorStatus().Paused {
		t.Errorf("resume: %v", err)
	}
	if err := w.d.ResetBaseline(ctx, ""); err != nil {
		t.Error(err)
	}
	if r := w.mon.Resets(); len(r) != 1 || r[0] != "wifi:"+fake.HomeSSID {
		t.Errorf("resets = %v", r)
	}
	waitChange(t, ch, core.ChangeMonitor, time.Second)
	w.mon.AddSample(core.Sample{NetworkKey: "wifi:" + fake.HomeSSID, Anchor: "gateway", RTTms: 3})
	if s, err := w.d.Samples(ctx, "", "gateway", 10); err != nil || len(s) != 1 {
		t.Errorf("samples = %v %v", s, err)
	}
	w.mon.Emit(core.Event{Type: core.EventDegraded})
	e := waitEvent(t, ch, core.EventDegraded, time.Second)
	if e.Title != "Network degraded" || e.NetworkKey != "wifi:"+fake.HomeSSID || e.Urgency != "normal" {
		t.Errorf("degraded = %+v", e)
	}
	w.mon.Emit(core.Event{Type: core.EventRecovered, Title: "custom"})
	e = waitEvent(t, ch, core.EventRecovered, time.Second)
	if e.Title != "custom" || e.Urgency != "low" {
		t.Errorf("recovered = %+v", e)
	}
	w.mon.SetStatus(core.MonitorStatus{NetworkKey: "x", State: core.BaselineDegraded})
	waitChange(t, ch, core.ChangeMonitor, time.Second)
	if w.d.MonitorStatus().State != core.BaselineDegraded {
		t.Error("monitor status change not cached")
	}
	w.nm.DisconnectAll()
	waitEvent(t, ch, core.EventDisconnected, time.Second)
	if err := w.d.ResetBaseline(ctx, ""); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("reset with nothing connected: %v", err)
	}
}

func TestSpeed(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var phases []string
	res, err := w.d.RunSpeed(ctx, core.SpeedOptions{}, func(p core.SpeedProgress) { phases = append(phases, p.Phase) })
	if err != nil {
		t.Fatal(err)
	}
	if res.NetworkKey != "wifi:"+fake.HomeSSID || res.Provider != "cloudflare" || len(phases) != 5 {
		t.Errorf("result = %+v phases %v", res, phases)
	}
	hist, err := w.d.SpeedHistory(ctx, "", 0)
	if err != nil || len(hist) != 1 {
		t.Errorf("history = %v %v", hist, err)
	}
	// only one at a time
	w.speed.Delay = 30 * time.Millisecond
	var wg sync.WaitGroup
	var conflict error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = w.d.RunSpeed(ctx, core.SpeedOptions{Quick: true}, nil)
	}()
	time.Sleep(10 * time.Millisecond)
	_, conflict = w.d.RunSpeed(ctx, core.SpeedOptions{}, nil)
	wg.Wait()
	if !errors.Is(conflict, core.ErrConflict) {
		t.Errorf("concurrent run: %v", conflict)
	}
	w.speed.Delay = 0
	w.speed.Err = errors.New("provider down")
	if _, err := w.d.RunSpeed(ctx, core.SpeedOptions{}, nil); err == nil {
		t.Error("want provider error")
	}
}

func TestConfigSetPersistsAndUpdatesPolicy(t *testing.T) {
	w := newWorld(t)
	cfg, err := w.d.SetConfig("notify.degraded", "true")
	if err != nil || !cfg.Notify.Degraded {
		t.Fatalf("set: %v %+v", err, cfg.Notify)
	}
	if !w.d.Config().Notify.Degraded {
		t.Error("config not swapped")
	}
	loaded, err := config.LoadFrom(w.d.o.ConfigPath)
	if err != nil || !loaded.Notify.Degraded {
		t.Errorf("not persisted: %v %+v", err, loaded.Notify)
	}
	if _, err := w.d.SetConfig("notify.degraded", "maybe"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad value: %v", err)
	}
	if _, err := w.d.SetConfig("nope", "1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("bad key: %v", err)
	}
	if w.d.Config().Notify.Degraded != true {
		t.Error("failed set must not change config")
	}
}

func TestNotifyTest(t *testing.T) {
	w := newWorld(t)
	if err := w.d.NotifyTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := w.notifier.Events()
	if len(evs) != 1 || evs[0].Data["test"] != "true" {
		t.Errorf("notified = %+v", evs)
	}
	if stored, _ := w.store.Events(context.Background(), 0); len(stored) != 0 {
		t.Error("test notifications must not be stored")
	}
}

func TestSlowSubscriberDropsNotBlocks(t *testing.T) {
	w := newWorld(t)
	ch, cancel := w.d.Subscribe()
	defer cancel()
	for i := 0; i < subscriberBuffer*3; i++ {
		w.nm.Emit(core.Change{Kind: core.ChangeWifi})
	}
	// the daemon loop must still be alive
	w.nm.DisconnectAll()
	deadline := time.Now().Add(2 * time.Second)
	for w.d.Status().Primary != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if w.d.Status().Primary != nil {
		t.Fatal("daemon loop stalled behind a slow subscriber")
	}
	n := 0
	for len(ch) > 0 {
		<-ch
		n++
	}
	if n > subscriberBuffer {
		t.Errorf("buffer overflowed: %d", n)
	}
}

func TestRunTwiceFails(t *testing.T) {
	w := newWorld(t)
	if err := w.d.Run(context.Background()); err == nil {
		t.Error("second Run should fail")
	}
}

func TestCloseClosesStore(t *testing.T) {
	w := newWorld(t)
	if err := w.d.Close(); err != nil || !w.store.Closed() {
		t.Error("close")
	}
}

func TestRunFailsWhenNMUnreadable(t *testing.T) {
	nm := fake.NewNM()
	nm.Fail("Status", errors.New("bus gone"))
	d, _ := New(Options{NM: nm, Store: fake.NewStore(), Logger: slog.New(slog.DiscardHandler)})
	if err := d.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "bus gone") {
		t.Errorf("Run = %v", err)
	}
}
