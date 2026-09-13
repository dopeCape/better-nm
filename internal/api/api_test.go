package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
	"github.com/dopeCape/better-nm/internal/diag"
	"github.com/dopeCape/better-nm/internal/fake"
	"github.com/dopeCape/better-nm/internal/version"
)

type rig struct {
	t        *testing.T
	c        *client.Client
	d        *daemon.Daemon
	nm       *fake.NM
	ts       *fake.VPNAdapter
	mon      *fake.Monitor
	store    *fake.Store
	notifier *fake.Notifier
	speed    *fake.SpeedTester
	diag     *fake.Diag
	socket   string
	cfgPath  string
}

// shortTempDir avoids the 108-byte Unix socket path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bnm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newRig(t *testing.T) *rig { return newRigWith(t, nil) }

// newRigWith lets a test adjust the daemon's options before it starts.
func newRigWith(t *testing.T, tweak func(*daemon.Options)) *rig {
	t.Helper()
	r := &rig{
		t:        t,
		nm:       fake.NewNM(),
		ts:       fake.NewTailscale(),
		mon:      fake.NewMonitor(),
		store:    fake.NewStore(),
		notifier: fake.NewNotifier(),
		speed:    fake.NewSpeedTester(),
		diag:     fake.NewDiag(),
	}
	dir := shortTempDir(t)
	r.socket = filepath.Join(dir, "bnmd.sock")
	r.cfgPath = filepath.Join(dir, "config.toml")
	t.Setenv("XDG_STATE_HOME", dir)
	opts := daemon.Options{
		NM:                r.nm,
		VPN:               fake.NewVPNRegistry(r.ts, fake.NewWireGuard(), fake.NewNMVPN()),
		Monitor:           r.mon,
		Store:             r.store,
		Speed:             r.speed,
		Notifier:          r.notifier,
		Diag:              r.diag,
		WireGuard:         &fake.WireGuardImporter{NM: r.nm},
		Config:            config.Default(),
		ConfigPath:        r.cfgPath,
		Logger:            slog.New(slog.DiscardHandler),
		Version:           "test-1",
		Debounce:          30 * time.Millisecond,
		ConnectivityGrace: 30 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&opts)
	}
	d, err := daemon.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	sock, err := daemon.Listen(r.socket)
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
	c, err := client.New(client.WithSocket(r.socket), client.WithAutoStart(false))
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	t.Cleanup(func() { c.Close() })
	return r
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestStatusAndVersion(t *testing.T) {
	r := newRig(t)
	st, err := r.c.Status(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "test-1" || st.APIVersion != version.APIVersion || st.UptimeSeconds < 0 || st.SnapshotVersion == 0 {
		t.Errorf("status = %+v", st)
	}
	if st.Primary == nil || st.NetworkKey != "wifi:"+fake.HomeSSID || st.Connectivity != core.ConnFull || !st.WifiEnabled {
		t.Errorf("nm status = %+v", st.Status)
	}
	// raw curl-style access sees the same JSON
	body, code, err := r.c.Raw(ctxT(t), http.MethodGet, "/status", nil)
	if err != nil || code != 200 || !strings.Contains(string(body), `"api_version":1`) {
		t.Errorf("raw = %d %s %v", code, body, err)
	}
}

func TestDevicesWifiProfilesActive(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	devs, err := r.c.Devices(ctx)
	if err != nil || len(devs) != 2 {
		t.Fatalf("devices = %v %v", devs, err)
	}
	nets, err := r.c.Wifi(ctx, "")
	if err != nil || len(nets) != 4 {
		t.Fatalf("wifi = %v %v", nets, err)
	}
	nets, err = r.c.Wifi(ctx, fake.WifiDevice)
	if err != nil || len(nets) != 4 {
		t.Fatalf("wifi(wlan0) = %v %v", nets, err)
	}
	if _, err = r.c.Wifi(ctx, "wlan9"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("wifi unknown = %v", err)
	}
	if err := r.c.ScanWifi(ctx, ""); err != nil {
		t.Error(err)
	}
	ps, err := r.c.Profiles(ctx)
	if err != nil || len(ps) != 3 {
		t.Fatalf("profiles = %v %v", ps, err)
	}
	p, err := r.c.Profile(ctx, fake.HomeUUID)
	if err != nil || p.Name != fake.HomeSSID || !p.Active {
		t.Errorf("profile = %+v %v", p, err)
	}
	if _, err := r.c.Profile(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("profile unknown = %v", err)
	}
	if err := r.c.SetProfileIP(ctx, fake.HomeUUID, &core.IPConfig{Method: core.IPManual, Addresses: []string{"10.0.0.7/24"}, Gateway: "10.0.0.1"}, nil); err != nil {
		t.Error(err)
	}
	p, _ = r.c.Profile(ctx, fake.HomeUUID)
	if p.IPv4.Method != core.IPManual || p.IPv4.Addresses[0] != "10.0.0.7/24" {
		t.Errorf("ip not applied: %+v", p.IPv4)
	}
	if err := r.c.SetProfileIP(ctx, fake.HomeUUID, nil, nil); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("empty ip = %v", err)
	}
	if err := r.c.SetAutoconnect(ctx, fake.OfficeUUID, false); err != nil {
		t.Error(err)
	}
	if err := r.c.ActivateProfile(ctx, fake.WiredUUID, ""); err != nil {
		t.Error(err)
	}
	acs, err := r.c.ActiveConnections(ctx)
	if err != nil || len(acs) != 2 {
		t.Errorf("active = %v %v", acs, err)
	}
	if err := r.c.DeactivateProfile(ctx, fake.WiredUUID); err != nil {
		t.Error(err)
	}
	if err := r.c.DeactivateProfile(ctx, fake.WiredUUID); !errors.Is(err, core.ErrConflict) {
		t.Errorf("deactivate twice = %v", err)
	}
	if err := r.c.DeleteProfile(ctx, fake.OfficeUUID); err != nil {
		t.Error(err)
	}
	if ps, _ = r.c.Profiles(ctx); len(ps) != 2 {
		t.Errorf("profiles after delete = %d", len(ps))
	}
	if err := r.c.DeleteProfile(ctx, fake.OfficeUUID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
}

func TestWifiConnectDisconnectForgetEnable(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	if err := r.c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: fake.NeighbourSSD}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("no password = %v", err)
	}
	if err := r.c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: fake.NeighbourSSD, Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	st, _ := r.c.Status(ctx)
	if st.NetworkKey != "wifi:"+fake.NeighbourSSD {
		t.Errorf("key = %s", st.NetworkKey)
	}
	ps, _ := r.c.Profiles(ctx)
	var newUUID string
	for _, p := range ps {
		if p.SSID == fake.NeighbourSSD {
			newUUID = p.UUID
		}
	}
	if newUUID == "" {
		t.Fatal("profile not created")
	}
	if err := r.c.DisconnectWifi(ctx, ""); err != nil {
		t.Error(err)
	}
	if st, _ = r.c.Status(ctx); st.Primary != nil {
		t.Error("still connected")
	}
	if err := r.c.ForgetWifi(ctx, newUUID); err != nil {
		t.Error(err)
	}
	if err := r.c.ForgetWifi(ctx, ""); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("forget empty = %v", err)
	}
	if err := r.c.SetWifiEnabled(ctx, false); err != nil {
		t.Error(err)
	}
	if st, _ = r.c.Status(ctx); st.WifiEnabled {
		t.Error("wifi still enabled")
	}
	if err := r.c.SetWifiEnabled(ctx, true); err != nil {
		t.Error(err)
	}
}

func TestVPNRoutes(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	vs, err := r.c.VPNs(ctx)
	if err != nil || len(vs) != 3 {
		t.Fatalf("vpns = %v %v", vs, err)
	}
	if err := r.c.ConnectVPN(ctx, "tailscale"); err != nil {
		t.Fatal(err)
	}
	vs, _ = r.c.VPNs(ctx)
	if vs[0].State != core.VPNConnected {
		t.Errorf("state = %s", vs[0].State)
	}
	if err := r.c.DisconnectVPN(ctx, "tailscale"); err != nil {
		t.Error(err)
	}
	if err := r.c.ConnectVPN(ctx, "ghost"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown vpn = %v", err)
	}
	if err := r.c.SetTailscaleExitNode(ctx, "homeserver", true); err != nil {
		t.Error(err)
	}
	if err := r.c.SetTailscaleExitNode(ctx, "phone", false); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad exit node = %v", err)
	}
	if err := r.c.SetTailscaleExitNodeEnabled(ctx, false); err != nil {
		t.Error(err)
	}
	if err := r.c.SetTailscaleAcceptDNS(ctx, false); err != nil {
		t.Error(err)
	}
	u, err := r.c.TailscaleLogin(ctx)
	if err != nil || !strings.HasPrefix(u, "https://") {
		t.Errorf("login = %q %v", u, err)
	}
	if err := r.c.TailscaleLogout(ctx); err != nil {
		t.Error(err)
	}
	res, err := r.c.ImportVPN(ctx, api.ImportVPNRequest{Kind: "wireguard", Name: "wg-imp", Content: "[Interface]\nPrivateKey = k=\nAddress = 10.9.0.2/24\n"})
	if err != nil || res.UUID == "" || res.Name != "wg-imp" {
		t.Errorf("import wg = %+v %v", res, err)
	}
	res, err = r.c.ImportVPN(ctx, api.ImportVPNRequest{Kind: "openvpn", Content: "client\n", Name: "ovpn-imp"})
	if err != nil || res.UUID == "" {
		t.Errorf("import ovpn = %+v %v", res, err)
	}
	if _, err = r.c.ImportVPN(ctx, api.ImportVPNRequest{Kind: "l2tp", Content: "x"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("import bad kind = %v", err)
	}
	// permission mapping with hint
	r.ts.Fail("Connect", core.Errorf(core.KindPermission, "run: sudo tailscale set --operator=$USER", "tailscale: not operator"))
	err = r.c.ConnectVPN(ctx, "tailscale")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != core.KindPermission || !strings.Contains(apiErr.Hint, "operator") {
		t.Errorf("permission error = %+v", err)
	}
	if !errors.Is(err, core.ErrPermission) {
		t.Error("errors.Is(core.ErrPermission) should hold")
	}
}

func TestMonitorRoutes(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	ms, err := r.c.Monitor(ctx)
	if err != nil || ms.NetworkKey != "wifi:"+fake.HomeSSID || len(ms.Anchors) != 2 {
		t.Fatalf("monitor = %+v %v", ms, err)
	}
	r.mon.AddSample(core.Sample{Time: time.Now(), NetworkKey: "wifi:" + fake.HomeSSID, Anchor: "gateway", RTTms: 2.5})
	r.mon.AddSample(core.Sample{Time: time.Now(), NetworkKey: "wifi:" + fake.HomeSSID, Anchor: "1.1.1.1", RTTms: 12})
	ss, err := r.c.Samples(ctx, "", "", 0)
	if err != nil || len(ss) != 2 {
		t.Errorf("samples = %v %v", ss, err)
	}
	ss, _ = r.c.Samples(ctx, "wifi:"+fake.HomeSSID, "gateway", 5)
	if len(ss) != 1 || ss[0].RTTms != 2.5 {
		t.Errorf("filtered samples = %v", ss)
	}
	if _, code, _ := r.c.Raw(ctx, http.MethodGet, "/monitor/samples?limit=x", nil); code != 400 {
		t.Errorf("bad limit = %d", code)
	}
	if err := r.c.ResetBaseline(ctx, ""); err != nil {
		t.Error(err)
	}
	if err := r.c.PauseMonitor(ctx); err != nil {
		t.Error(err)
	}
	if ms, _ = r.c.Monitor(ctx); !ms.Paused {
		t.Error("not paused")
	}
	if err := r.c.ResumeMonitor(ctx); err != nil {
		t.Error(err)
	}
	if ms, _ = r.c.Monitor(ctx); ms.Paused {
		t.Error("not resumed")
	}
}

func TestSpeedStreamAndWait(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	var phases []string
	res, err := r.c.Speed(ctx, core.SpeedOptions{Quick: true}, func(p core.SpeedProgress) { phases = append(phases, p.Phase) })
	if err != nil {
		t.Fatal(err)
	}
	if res.DownloadMbps == 0 || !res.Quick || res.NetworkKey != "wifi:"+fake.HomeSSID || len(phases) != 5 || phases[0] != "latency" {
		t.Errorf("result = %+v phases %v", res, phases)
	}
	res, err = r.c.Speed(ctx, core.SpeedOptions{Provider: "iperf3", Server: "h:5201"}, nil)
	if err != nil || res.Provider != "iperf3" || res.Server != "h:5201" {
		t.Errorf("wait result = %+v %v", res, err)
	}
	hist, err := r.c.SpeedHistory(ctx, "", 0)
	if err != nil || len(hist) != 2 {
		t.Errorf("history = %v %v", hist, err)
	}
	hist, _ = r.c.SpeedHistory(ctx, "wifi:nope", 1)
	if len(hist) != 0 {
		t.Errorf("history filter = %v", hist)
	}
	r.speed.Err = core.Errorf(core.KindUnavailable, "check your connection", "speed: cloudflare unreachable")
	_, err = r.c.Speed(ctx, core.SpeedOptions{}, func(core.SpeedProgress) {})
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != core.KindUnavailable || apiErr.Hint == "" {
		t.Errorf("stream error = %+v", err)
	}
	_, err = r.c.Speed(ctx, core.SpeedOptions{}, nil)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Errorf("wait error = %+v", err)
	}
}

func TestSpeedStreamRawFormat(t *testing.T) {
	r := newRig(t)
	body, code, err := r.c.Raw(ctxT(t), http.MethodPost, "/speed", core.SpeedOptions{})
	if err != nil || code != 200 {
		t.Fatalf("raw speed = %d %v", code, err)
	}
	s := string(body)
	if !strings.Contains(s, "event: progress\ndata: {") || !strings.Contains(s, "event: result\ndata: {") {
		t.Errorf("unexpected SSE body:\n%s", s)
	}
}

func TestEventsStreamAndHistory(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := r.c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// give the subscription a moment to attach before mutating
	deadline := time.Now().Add(time.Second)
	for r.d.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	r.nm.DisconnectAll()
	var gotChange, gotEvent bool
	timeout := time.After(3 * time.Second)
	for !gotEvent {
		select {
		case it, ok := <-stream:
			if !ok {
				t.Fatal("stream closed")
			}
			if it.Change != nil && it.Change.Kind == core.ChangeActive {
				gotChange = true
			}
			if it.Event != nil && it.Event.Type == core.EventDisconnected {
				gotEvent = true
				if it.Event.Title != "Disconnected from "+fake.HomeSSID {
					t.Errorf("event = %+v", it.Event)
				}
			}
		case <-timeout:
			t.Fatal("no disconnected event on the stream")
		}
	}
	if !gotChange {
		t.Error("no active change on the stream")
	}
	hist, err := r.c.EventHistory(ctxT(t), 10)
	if err != nil || len(hist) != 1 || hist[0].Type != core.EventDisconnected {
		t.Errorf("history = %+v %v", hist, err)
	}
	// heartbeats keep flowing and do not surface as items
	select {
	case it := <-stream:
		if it.Event != nil {
			t.Errorf("unexpected event %+v", it.Event)
		}
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case _, ok := <-stream:
		for ok {
			_, ok = <-stream
		}
	case <-time.After(2 * time.Second):
		t.Error("stream did not close after cancel")
	}
}

func TestEventStreamRawHeartbeat(t *testing.T) {
	r := newRig(t)
	conn, err := net.Dial("unix", r.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /v1/events/stream HTTP/1.1\r\nHost: bnmd\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	var got string
	for !strings.Contains(got, ": ping") {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got)
		}
		got += string(buf[:n])
	}
	if !strings.Contains(got, "Content-Type: text/event-stream") || !strings.Contains(got, ": connected") {
		t.Errorf("headers/prelude missing:\n%s", got)
	}
}

// A refused ping socket (Debian/Ubuntu without ping_group_range) skips the
// sweep but the neighbour table is still valid: the route must answer 200
// with the hosts rather than fail the whole call.
func TestDiagLANSweepSkippedStillReturnsHosts(t *testing.T) {
	fakeDiag := fake.NewDiag()
	r := newRigWith(t, func(o *daemon.Options) {
		o.Diag = daemon.DiagFuncs{
			LANHostsFn: func(ctx context.Context, device string, sweep bool) ([]core.LANHost, error) {
				hosts, _ := fakeDiag.LANHosts(ctx, device, false)
				return hosts, &diag.SweepError{Err: diag.ErrNeedsPingGroup}
			},
		}
	})
	hosts, err := r.c.LANHosts(ctxT(t), "", true)
	if err != nil {
		t.Fatalf("lan with a skipped sweep: %v", err)
	}
	if len(hosts) != 3 {
		t.Errorf("hosts = %+v, want the 3 table entries", hosts)
	}
	// Any other error still fails the call with its kind.
	fakeDiag.Err = core.Errorf(core.KindNotFound, "", "no such device")
	r2 := newRigWith(t, func(o *daemon.Options) { o.Diag = fakeDiag })
	if _, err := r2.c.LANHosts(ctxT(t), "", true); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v, want not-found", err)
	}
}

func TestDiagRoutes(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	if h, err := r.c.LANHosts(ctx, "", true); err != nil || len(h) != 4 {
		t.Errorf("lan = %v %v", h, err)
	}
	if p, err := r.c.ListeningPorts(ctx); err != nil || len(p) != 3 {
		t.Errorf("ports = %v %v", p, err)
	}
	if rt, err := r.c.Routes(ctx); err != nil || len(rt) != 3 {
		t.Errorf("routes = %v %v", rt, err)
	}
	a, err := r.c.DNSLookup(ctx, "example.com", "1.1.1.1", "AAAA")
	if err != nil || a.Type != "AAAA" || a.Server != "1.1.1.1" || len(a.Answers) != 1 {
		t.Errorf("dns = %+v %v", a, err)
	}
	if _, err := r.c.DNSLookup(ctx, "", "", ""); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("dns empty = %v", err)
	}
	if ip, err := r.c.PublicIP(ctx); err != nil || ip.IP == "" {
		t.Errorf("public ip = %+v %v", ip, err)
	}
	if inf, err := r.c.Infra(ctx); err != nil || len(inf) != 1 {
		t.Errorf("infra = %v %v", inf, err)
	}
	r.diag.Err = core.Errorf(core.KindUnsupported, "install iproute2", "diag: ip not found")
	_, err = r.c.Routes(ctx)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotImplemented || !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("unsupported = %+v", err)
	}
}

func TestConfigRoutes(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	cfg, err := r.c.Config(ctx)
	if err != nil || cfg.Speed.Provider != "cloudflare" || cfg.Notify.Degraded {
		t.Fatalf("config = %+v %v", cfg, err)
	}
	cfg, err = r.c.SetConfig(ctx, "notify.degraded", "true")
	if err != nil || !cfg.Notify.Degraded {
		t.Errorf("set = %+v %v", cfg, err)
	}
	if _, err := os.Stat(r.cfgPath); err != nil {
		t.Errorf("config not written: %v", err)
	}
	if _, err = r.c.SetConfig(ctx, "notify.degraded", "sometimes"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad value = %v", err)
	}
	if _, err = r.c.SetConfig(ctx, "", "1"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("empty key = %v", err)
	}
	if _, err = r.c.SetConfig(ctx, "nope.x", "1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown key = %v", err)
	}
}

func TestNotifyTest(t *testing.T) {
	r := newRig(t)
	if err := r.c.NotifyTest(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if evs := r.notifier.Events(); len(evs) != 1 || evs[0].Data["test"] != "true" {
		t.Errorf("notified = %+v", evs)
	}
}

func TestErrorMappingAndBadInput(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
		code   core.ErrorKind
	}{
		{"no route", http.MethodGet, "/nope", nil, 404, core.KindNotFound},
		{"wrong method", http.MethodDelete, "/status", nil, 405, ""},
		{"bad json", http.MethodPost, "/wifi/connect", []byte(`{"ssid":`), 400, core.KindInvalid},
		{"unknown field", http.MethodPost, "/wifi/connect", map[string]any{"ssid": "x", "bogus": 1}, 400, core.KindInvalid},
		{"profile missing", http.MethodGet, "/profiles/zzz", nil, 404, core.KindNotFound},
		{"vpn missing", http.MethodPost, "/vpn/zzz/disconnect", nil, 404, core.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, code, err := r.c.Raw(ctx, tt.method, tt.path, tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", code, tt.want, body)
			}
			if tt.code == "" {
				return
			}
			var er api.ErrorResponse
			if err := json.Unmarshal(body, &er); err != nil || er.Code != string(tt.code) || er.Error == "" {
				t.Errorf("body = %s", body)
			}
		})
	}
	// typed backend errors surface with their status and hint
	r.nm.Fail("Scan", core.Errorf(core.KindPermission, "add yourself to the netdev group", "nm: scan not authorised"))
	err := r.c.ScanWifi(ctx, "")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || apiErr.Hint != "add yourself to the netdev group" {
		t.Errorf("scan permission = %+v", err)
	}
	r.nm.Fail("Scan", nil)
	r.nm.Fail("ConnectWifi", errors.New("plain failure"))
	err = r.c.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: fake.CafeSSID})
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Code != core.KindInternal {
		t.Errorf("internal = %+v", err)
	}
	// a permission error without a hint gets the generic polkit hint
	r.nm.Fail("SetWifiEnabled", core.ErrPermission)
	err = r.c.SetWifiEnabled(ctx, false)
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || !strings.Contains(apiErr.Hint, "polkit") {
		t.Errorf("generic permission hint = %+v", err)
	}
}

func TestListsNeverNull(t *testing.T) {
	r := newRig(t)
	ctx := ctxT(t)
	r.mon.SetStatus(core.MonitorStatus{State: core.BaselineIdle})
	for _, path := range []string{"/events", "/speed/history", "/monitor/samples", "/monitor"} {
		body, _, err := r.c.Raw(ctx, http.MethodGet, path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "null") && !strings.Contains(path, "monitor") {
			t.Errorf("%s returned null: %s", path, body)
		}
		if path == "/monitor" && !strings.Contains(string(body), `"anchors":[]`) {
			t.Errorf("/monitor anchors should be []: %s", body)
		}
	}
}

func TestVersionMismatchRefused(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"api_version": version.APIVersion + 1, "version": "9.9.9"})
	})
	go func() { _ = http.Serve(ln, mux) }()
	_, err = client.New(client.WithSocket(sock), client.WithAutoStart(false))
	var vm *client.ErrVersionMismatch
	if !errors.As(err, &vm) || vm.Daemon != version.APIVersion+1 || vm.DaemonVersion != "9.9.9" {
		t.Fatalf("want version mismatch, got %v", err)
	}
	if !strings.Contains(err.Error(), "restart") {
		t.Errorf("message should say what to do: %v", err)
	}
	if _, err := client.New(client.WithSocket(sock), client.WithAutoStart(false), client.WithVersionCheck(false)); err != nil {
		t.Errorf("skip check: %v", err)
	}
}

func TestNotRunningWithoutAutoStart(t *testing.T) {
	dir := shortTempDir(t)
	_, err := client.New(client.WithSocket(filepath.Join(dir, "none.sock")), client.WithAutoStart(false))
	if !client.IsNotRunning(err) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}
