package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"github.com/dopeCape/better-nm/internal/fake"
)

// rig is an in-process bnmd on a temp socket, backed by internal/fake.
type rig struct {
	t      *testing.T
	d      *daemon.Daemon
	nm     *fake.NM
	ts     *fake.VPNAdapter
	wg     *fake.VPNAdapter
	mon    *fake.Monitor
	store  *fake.Store
	speed  *fake.SpeedTester
	socket string
	dir    string
}

// shortTempDir avoids the 108-byte Unix socket path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bnmcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		t:     t,
		nm:    fake.NewNM(),
		ts:    fake.NewTailscale(),
		wg:    fake.NewWireGuard(),
		mon:   fake.NewMonitor(),
		store: fake.NewStore(),
		speed: fake.NewSpeedTester(),
	}
	r.dir = shortTempDir(t)
	r.socket = filepath.Join(r.dir, "bnmd.sock")
	t.Setenv("XDG_STATE_HOME", r.dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(r.dir, "cfg"))
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	d, err := daemon.New(daemon.Options{
		NM:                r.nm,
		VPN:               fake.NewVPNRegistry(r.ts, r.wg, fake.NewNMVPN()),
		Monitor:           r.mon,
		Store:             r.store,
		Speed:             r.speed,
		Notifier:          fake.NewNotifier(),
		Diag:              fake.NewDiag(),
		WireGuard:         &fake.WireGuardImporter{NM: r.nm},
		Config:            config.Default(),
		ConfigPath:        filepath.Join(r.dir, "config.toml"),
		Logger:            slog.New(slog.DiscardHandler),
		Version:           "test-1",
		Debounce:          20 * time.Millisecond,
		ConnectivityGrace: 20 * time.Millisecond,
	})
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
	return r
}

type result struct {
	out, err string
	code     int
}

// run executes bnm against the rig's socket.
func (r *rig) run(args ...string) result {
	return r.runIn(context.Background(), "", args...)
}

func (r *rig) runIn(ctx context.Context, stdin string, args ...string) result {
	r.t.Helper()
	var out, errb bytes.Buffer
	full := append([]string{"--socket", r.socket, "--no-autostart"}, args...)
	code := Run(ctx, full, strings.NewReader(stdin), &out, &errb)
	return result{out: out.String(), err: errb.String(), code: code}
}

func (r *rig) ok(args ...string) result {
	r.t.Helper()
	res := r.run(args...)
	if res.code != 0 {
		r.t.Fatalf("bnm %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), res.code, res.out, res.err)
	}
	return res
}

func (r *rig) jsonInto(v any, args ...string) {
	r.t.Helper()
	res := r.ok(append(args, "--json")...)
	if err := json.Unmarshal([]byte(res.out), v); err != nil {
		r.t.Fatalf("bnm %s --json: %v\n%s", strings.Join(args, " "), err, res.out)
	}
}

func wants(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("output lacks %q:\n%s", s, got)
		}
	}
}

func hasANSI(s string) bool { return strings.Contains(s, "\x1b[") }

// --- tests ---------------------------------------------------------------------------------------

func TestStatus(t *testing.T) {
	r := newRig(t)
	res := r.ok("status")
	wants(t, res.out, "Connected to HomeNet", "wifi via wlan0", "192.168.1.42/24", "192.168.1.1", "Internet  full",
		"VPN       none up", "Monitor", "bnmd test-1", "NM 1.46.0-fake", "82%", "5 GHz ch 36", "WPA2")
	if hasANSI(res.out) {
		t.Error("non-TTY output must not carry colour")
	}
	var js statusJSON
	r.jsonInto(&js, "status")
	if js.Status.NetworkKey != "wifi:HomeNet" || js.Status.Version != "test-1" || len(js.VPNs) != 3 {
		t.Errorf("status json = %+v", js)
	}

	// Disconnected world.
	if err := r.nm.DisconnectDevice(context.Background(), fake.WifiDevice); err != nil {
		t.Fatal(err)
	}
	r.d.Refresh(context.Background())
	res = r.ok("status")
	wants(t, res.out, "Not connected")
}

func TestDevices(t *testing.T) {
	r := newRig(t)
	res := r.ok("devices")
	wants(t, res.out, "DEVICE", "wlan0", "wifi", "connected", "192.168.1.42/24", "HomeNet", "eth0", "ethernet", "disconnected")
	var devs []core.Device
	r.jsonInto(&devs, "devices")
	if len(devs) != 2 {
		t.Errorf("devices json = %d", len(devs))
	}
}

func TestWifiList(t *testing.T) {
	r := newRig(t)
	res := r.ok("wifi", "list")
	wants(t, res.out, "SSID", "SIGNAL", "BAND", "SECURITY", "HomeNet", "82%", "5 GHz/36", "WPA2", "connected",
		"CoffeeShop", "open", "Neighbour5G", "WPA3", "Office", "802.1X", "saved", "▂▄▆█")
	lines := strings.Split(strings.TrimSpace(res.out), "\n")
	if !strings.Contains(lines[1], "HomeNet") {
		t.Errorf("active network should come first:\n%s", res.out)
	}
	for _, l := range lines {
		if len([]rune(l)) > 80 {
			t.Errorf("line wider than 80 columns: %q", l)
		}
	}
	var nets []core.WifiNetwork
	r.jsonInto(&nets, "wifi", "list")
	if len(nets) != 4 || !nets[0].Active {
		t.Errorf("wifi json = %+v", nets)
	}

	start := time.Now()
	res = r.ok("wifi", "list", "--rescan")
	if time.Since(start) > scanWait {
		t.Error("--rescan did not return on the wifi change")
	}
	wants(t, res.out, "HomeNet", "81%") // the fake nudges strengths down on scan

	res = r.run("wifi", "list", "--device", "eth0")
	if res.code != ExitError || !strings.Contains(res.err, "not a Wi-Fi device") {
		t.Errorf("bad device: %d %q", res.code, res.err)
	}
}

func TestWifiConnect(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res := r.ok("wifi", "connect", fake.CafeSSID)
	wants(t, res.out, "Connected to CoffeeShop")
	st, _ := r.nm.Status(ctx)
	if st.Primary == nil || st.Primary.ProfileName != fake.CafeSSID {
		t.Errorf("primary after connect = %+v", st.Primary)
	}

	// A secured, unknown network needs a password; without a TTY that is an error with a hint.
	res = r.run("wifi", "connect", fake.NeighbourSSD)
	if res.code != ExitError {
		t.Errorf("exit = %d", res.code)
	}
	wants(t, res.err, `error: "Neighbour5G" needs a password`, "hint: pass --password or --ask")

	// --ask reads it from stdin.
	res = r.runIn(ctx, "hunter2\n", "wifi", "connect", fake.NeighbourSSD, "--ask")
	if res.code != 0 {
		t.Fatalf("--ask: %d %s", res.code, res.err)
	}
	wants(t, res.err, "Password for Neighbour5G:")
	wants(t, res.out, "Connected to Neighbour5G")

	// Wrong password as NM would report it: message plus hint, exit 1.
	r.nm.Fail("ConnectWifi", core.Errorf(core.KindInvalid, "check the password and try again", "nm: secrets were required, but not provided"))
	res = r.run("wifi", "connect", fake.CafeSSID, "--password", "nope")
	if res.code != ExitError {
		t.Errorf("exit = %d", res.code)
	}
	wants(t, res.err, "error: nm: secrets were required, but not provided", "hint: check the password and try again")
	if res.out != "" {
		t.Errorf("stdout should be empty on error: %q", res.out)
	}

	// A polkit refusal is exit 4.
	r.nm.Fail("ConnectWifi", core.Errorf(core.KindPermission, "run: pkexec ...", "nm: not authorized"))
	res = r.run("wifi", "connect", fake.CafeSSID)
	if res.code != ExitPermission {
		t.Errorf("permission exit = %d: %s", res.code, res.err)
	}
	r.nm.Fail("ConnectWifi", nil)

	// An SSID that is not in range (after one rescan) is refused before NM
	// is asked, since NM would treat it as hidden and keep the profile it
	// creates for it; --hidden says that is intended.
	r.nm.Fail("ConnectWifi", errors.New("nm must not be asked to connect to an unseen network"))
	res = r.run("wifi", "connect", "Ghost")
	if res.code != ExitError {
		t.Errorf("unseen ssid: exit %d", res.code)
	}
	wants(t, res.err, `error: "Ghost" is not in range`, "hint: check `bnm wifi list`, or pass --hidden")
	if res.out != "" {
		t.Errorf("stdout should be empty: %q", res.out)
	}
	r.nm.Fail("ConnectWifi", nil)
	res = r.run("wifi", "connect", "Ghost", "--hidden", "--password", "secret12")
	if res.code != 0 {
		t.Errorf("--hidden should reach NM: exit %d %s", res.code, res.err)
	}
	// Quiet and JSON action output.
	res = r.ok("wifi", "connect", fake.CafeSSID, "-q")
	if res.out != "" {
		t.Errorf("-q printed %q", res.out)
	}
	res = r.ok("wifi", "connect", fake.CafeSSID, "--json")
	var okr map[string]any
	if err := json.Unmarshal([]byte(res.out), &okr); err != nil || okr["ok"] != true {
		t.Errorf("json action = %s %v", res.out, err)
	}
}

func TestWifiSavedForgetOnOff(t *testing.T) {
	r := newRig(t)
	res := r.ok("wifi", "saved")
	wants(t, res.out, "HomeNet", "Office", "WPA2", "802.1X", "11111111", "22222222")

	res = r.ok("wifi", "forget", "Office")
	wants(t, res.out, "Forgot Office")
	res = r.ok("wifi", "saved")
	if strings.Contains(res.out, "Office") {
		t.Error("Office should be gone")
	}
	res = r.run("wifi", "forget", "Office")
	if res.code != ExitError || !strings.Contains(res.err, `no Wi-Fi profile matches "Office"`) || !strings.Contains(res.err, "hint: known: HomeNet") {
		t.Errorf("forget missing: %d %q", res.code, res.err)
	}
	// wired profiles are not Wi-Fi profiles
	res = r.run("wifi", "forget", "Wired connection 1")
	if res.code != ExitError {
		t.Errorf("forget wired: %d", res.code)
	}

	r.ok("wifi", "off")
	st, _ := r.nm.Status(context.Background())
	if st.WifiEnabled {
		t.Error("wifi should be off")
	}
	res = r.ok("wifi", "on")
	wants(t, res.out, "Wi-Fi on")
	r.ok("wifi", "disconnect")
}

func TestWired(t *testing.T) {
	r := newRig(t)
	res := r.ok("wired", "list")
	wants(t, res.out, "eth0", "disconnected", "Wired connection 1", "33333333")
	res = r.ok("wired", "up")
	wants(t, res.out, "Wired connection 1 up")
	res = r.ok("wired", "list")
	wants(t, res.out, "● ")
	res = r.ok("wired", "down")
	wants(t, res.out, "eth0 down")
	res = r.run("wired", "down")
	if res.code != ExitError || !strings.Contains(res.err, "no ethernet device is connected") {
		t.Errorf("down twice: %d %q", res.code, res.err)
	}
}

func TestProfiles(t *testing.T) {
	r := newRig(t)
	res := r.ok("profile", "list")
	wants(t, res.out, "NAME", "HomeNet", "Office", "Wired connection 1", "ethernet", "wifi")
	res = r.ok("profile", "list", "--type", "ethernet")
	if strings.Contains(res.out, "HomeNet") {
		t.Error("--type should filter")
	}
	res = r.ok("profile", "show", "1111")
	wants(t, res.out, "HomeNet active", fake.HomeUUID, "Type         wifi (802-11-wireless)", "IPv4         auto")

	r.ok("profile", "ip", "Office", "--ipv4-method", "manual", "--address", "10.0.0.5/24", "--gateway", "10.0.0.1", "--dns", "10.0.0.1,10.0.0.2")
	var p core.Profile
	r.jsonInto(&p, "profile", "show", "Office")
	if p.IPv4.Method != core.IPManual || len(p.IPv4.Addresses) != 1 || p.IPv4.Gateway != "10.0.0.1" || len(p.IPv4.DNS) != 2 || p.IPv6.Method != core.IPAuto {
		t.Errorf("ip config = %+v %+v", p.IPv4, p.IPv6)
	}
	res = r.run("profile", "ip", "Office", "--ipv4-method", "manual")
	if res.code != ExitUsage {
		t.Errorf("manual without address: %d %s", res.code, res.err)
	}
	res = r.run("profile", "ip", "Office")
	if res.code != ExitUsage {
		t.Errorf("no method: %d", res.code)
	}

	res = r.ok("profile", "autoconnect", "Office", "off")
	wants(t, res.out, "Autoconnect off for Office")
	r.jsonInto(&p, "profile", "show", "Office")
	if p.Autoconnect {
		t.Error("autoconnect should be off")
	}
	res = r.run("profile", "autoconnect", "Office", "maybe")
	if res.code != ExitUsage {
		t.Errorf("bad on/off: %d", res.code)
	}

	res = r.ok("profile", "delete", "Wired")
	wants(t, res.out, "Deleted Wired connection 1")
	var ps []core.Profile
	r.jsonInto(&ps, "profile", "list")
	if len(ps) != 2 {
		t.Errorf("profiles after delete = %d", len(ps))
	}
}

func TestNameResolution(t *testing.T) {
	pool := []candidate{
		{ID: "11111111-1111-4111-8111-111111111111", Name: "HomeNet", Kind: "wifi profile", SSID: "HomeNet"},
		{ID: "22222222-2222-4222-8222-222222222222", Name: "Office", Kind: "wifi profile", SSID: "Office"},
		{ID: "22222222-aaaa-4222-8222-222222222222", Name: "office", Kind: "wired profile"},
		{ID: "tailscale", Name: "Tailscale", Kind: "VPN"},
	}
	tests := []struct {
		arg     string
		wantID  string
		wantErr string
	}{
		{"HomeNet", "11111111-1111-4111-8111-111111111111", ""},
		{"homenet", "11111111-1111-4111-8111-111111111111", ""},
		{"1111", "11111111-1111-4111-8111-111111111111", ""},
		{"tailscale", "tailscale", ""},
		{"Office", "22222222-2222-4222-8222-222222222222", ""}, // exact beats case-insensitive
		{"office", "22222222-aaaa-4222-8222-222222222222", ""},
		{"OFFICE", "", "ambiguous"},
		{"2222", "", "ambiguous"},
		{"22222222-a", "22222222-aaaa-4222-8222-222222222222", ""},
		{"Ho", "11111111-1111-4111-8111-111111111111", ""}, // unique name prefix
		{"nope", "", `no thing matches "nope"`},
		{"", "", "required"},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			got, err := resolve(tt.arg, pool, "thing")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				var amb *ambiguousError
				if tt.wantErr == "ambiguous" {
					if !errors.As(err, &amb) || len(amb.candidates) != 2 {
						t.Errorf("ambiguous error = %v", err)
					}
					if core.HintOf(err) == "" {
						t.Error("ambiguity should carry a hint")
					}
				}
				return
			}
			if err != nil || got.ID != tt.wantID {
				t.Errorf("got %+v, %v; want %s", got, err, tt.wantID)
			}
		})
	}
}

func TestAmbiguityEndToEnd(t *testing.T) {
	r := newRig(t)
	// A WireGuard profile named like the Wi-Fi one makes "HomeNet" ambiguous for `profile`.
	imp := &fake.WireGuardImporter{NM: r.nm}
	conf := "[Interface]\nPrivateKey = abc=\nAddress = 10.0.0.2/24\n[Peer]\nPublicKey = def=\nAllowedIPs = 0.0.0.0/0\n"
	if _, err := imp.Import(context.Background(), fake.HomeSSID, strings.NewReader(conf)); err != nil {
		t.Fatal(err)
	}
	r.d.Refresh(context.Background())
	res := r.run("profile", "show", "HomeNet")
	if res.code != ExitError {
		t.Errorf("exit = %d", res.code)
	}
	wants(t, res.err, `error: "HomeNet" is ambiguous; it matches:`, fake.HomeUUID+"  HomeNet (wifi profile)", "HomeNet (WireGuard profile)", "hint: use the UUID")
	// The Wi-Fi-only pool is still unambiguous.
	r.ok("wifi", "forget", "HomeNet")
}

func TestVPN(t *testing.T) {
	r := newRig(t)
	res := r.ok("vpn", "list")
	wants(t, res.out, "NAME", "KIND", "BACKEND", "STATE", "Tailscale", "tailscale", "disconnected", "wg-home", "WireGuard", "vpn.example.com:51820", "office-ovpn", "OpenVPN", "nm-vpn")

	res = r.ok("vpn", "up", "wg-home")
	wants(t, res.out, "● wg-home connected")
	var vpns []core.VPN
	r.jsonInto(&vpns, "vpn", "list")
	for _, v := range vpns {
		if v.Name == "wg-home" && v.State != core.VPNConnected {
			t.Errorf("wg-home state = %s", v.State)
		}
	}
	res = r.ok("status")
	wants(t, res.out, "VPN       wg-home")

	res = r.ok("vpn", "down", "wg")
	wants(t, res.out, "○ wg-home disconnected")
	res = r.ok("vpn", "toggle", "ts")
	wants(t, res.out, "● Tailscale connected", "100.64.0.1")
	res = r.ok("vpn", "toggle", "tailscale", "--json")
	var v core.VPN
	if err := json.Unmarshal([]byte(res.out), &v); err != nil || v.State != core.VPNDisconnected {
		t.Errorf("toggle json = %v %+v", err, v)
	}

	res = r.run("vpn", "up", "nope")
	if res.code != ExitError || !strings.Contains(res.err, `no VPN matches "nope"`) || !strings.Contains(res.err, "hint: known: Tailscale, office-ovpn, wg-home") {
		t.Errorf("unknown vpn: %d %q", res.code, res.err)
	}
	r.wg.Fail("Connect", core.Errorf(core.KindPermission, "add yourself to the profile's permissions", "vpn: not allowed"))
	res = r.run("vpn", "up", "wg-home")
	if res.code != ExitPermission || !strings.Contains(res.err, "hint: add yourself") {
		t.Errorf("permission: %d %q", res.code, res.err)
	}
	r.wg.Fail("Connect", nil)

	// Slow backend: up waits for connecting to settle.
	r.wg.Latency = 150 * time.Millisecond
	res = r.ok("vpn", "up", "wg-home")
	wants(t, res.out, "wg-home connected")
	r.ok("vpn", "down", "wg-home")

	// A backend that accepts the request but ends in "error" is a failed
	// action in every output mode: -q and --json used to exit 0.
	const wgID = "44444444-4444-4444-8444-444444444444"
	r.wg.Latency = 3 * time.Second
	failSoon := func() {
		time.AfterFunc(50*time.Millisecond, func() { r.wg.SetState(wgID, core.VPNError) })
	}
	for _, flags := range [][]string{nil, {"-q"}, {"--json"}} {
		failSoon()
		res = r.run(append([]string{"vpn", "up", "wg-home"}, flags...)...)
		if res.code != ExitError {
			t.Errorf("vpn up %v ending in error: exit %d\nstdout %q\nstderr %q", flags, res.code, res.out, res.err)
		}
		wants(t, res.err, "error: wg-home failed")
		if len(flags) == 1 && flags[0] == "-q" && res.out != "" {
			t.Errorf("-q printed %q", res.out)
		}
		if len(flags) == 1 && flags[0] == "--json" && !strings.Contains(res.out, `"state": "error"`) {
			t.Errorf("--json should still print the final VPN: %q", res.out)
		}
		r.wg.SetState(wgID, core.VPNDisconnected)
	}
	r.wg.Latency = 0
}

func TestVPNAddAndTailscale(t *testing.T) {
	r := newRig(t)
	conf := filepath.Join(r.dir, "office.conf")
	if err := os.WriteFile(conf, []byte("[Interface]\nPrivateKey = abc=\nAddress = 10.0.0.2/24\n[Peer]\nPublicKey = def=\nEndpoint = vpn.example:51820\nAllowedIPs = 0.0.0.0/0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := r.ok("vpn", "add", "wg", conf)
	wants(t, res.out, "Imported office.conf as office (wireguard)")
	res = r.ok("profile", "list", "--type", "wireguard")
	wants(t, res.out, "office")
	res = r.run("vpn", "add", "wg", filepath.Join(r.dir, "missing.conf"))
	if res.code != ExitError {
		t.Errorf("missing file: %d", res.code)
	}

	res = r.ok("vpn", "ts", "status")
	wants(t, res.out, "Tailscale", "disconnected", "Backend     Stopped", "Tailnet     example.ts.net", "Peers       1 online of 2", "Operator    yes")
	res = r.ok("vpn", "ts", "peers")
	wants(t, res.out, "homeserver", "100.64.0.2", "linux", "offers", "phone", "android")
	var peers []core.TailscalePeer
	r.jsonInto(&peers, "vpn", "ts", "peers")
	if len(peers) != 2 {
		t.Errorf("peers json = %d", len(peers))
	}
	res = r.ok("vpn", "ts", "exit-node", "list")
	wants(t, res.out, "homeserver")
	if strings.Contains(res.out, "phone") {
		t.Error("phone offers no exit node")
	}
	res = r.ok("vpn", "ts", "exit-node", "set", "homeserver", "--allow-lan")
	wants(t, res.out, "exit node: homeserver")
	res = r.ok("vpn", "ts", "exit-node")
	wants(t, res.out, "exit node: homeserver (LAN access allowed)")
	res = r.ok("vpn", "ts", "exit-node", "off")
	wants(t, res.out, "exit node: off")
	res = r.ok("vpn", "ts", "dns", "off")
	wants(t, res.out, "accept DNS: off")
	res = r.ok("vpn", "ts", "login")
	wants(t, res.out, "https://login.tailscale.com/a/fake")
	res = r.ok("vpn", "ts", "logout")
	wants(t, res.out, "logged out")
}

func TestMonitor(t *testing.T) {
	r := newRig(t)
	now := time.Now()
	r.mon.SetStatus(core.MonitorStatus{
		NetworkKey: "wifi:HomeNet", State: core.BaselineOK, Interval: 30 * time.Second, LastSample: now,
		Anchors: []core.Baseline{
			{NetworkKey: "wifi:HomeNet", Anchor: "gateway", State: core.BaselineOK, SampleCount: 42, BaselineRTT: 2.1, CurrentRTT: 2.4, CurrentDNS: 8},
			{NetworkKey: "wifi:HomeNet", Anchor: "1.1.1.1", State: core.BaselineDegraded, SampleCount: 42, BaselineRTT: 12, CurrentRTT: 88, CurrentLoss: 0.1},
		},
	})
	time.Sleep(50 * time.Millisecond) // let the daemon pick up the change
	res := r.ok("monitor")
	wants(t, res.out, "wifi:HomeNet", "ok", "Interval     30s", "Last sample  just now", "ANCHOR", "gateway", "2.4 ms", "2.1 ms", "1.1.1.1", "degraded", "88 ms", "10%", "42")
	var ms core.MonitorStatus
	r.jsonInto(&ms, "monitor", "status")
	if ms.State != core.BaselineOK || len(ms.Anchors) != 2 {
		t.Errorf("monitor json = %+v", ms)
	}
	res = r.ok("status")
	wants(t, res.out, "Monitor   ok", "gateway 2.4 ms", "1.1.1.1 88 ms 10% loss")

	r.mon.AddSample(core.Sample{Time: now, NetworkKey: "wifi:HomeNet", Anchor: "gateway", RTTms: 2.4, Loss: 0, DNSms: 8, Method: "icmp"})
	r.mon.AddSample(core.Sample{Time: now, NetworkKey: "wifi:HomeNet", Anchor: "1.1.1.1", RTTms: -1, Loss: 1, DNSms: -1, Method: "tcp"})
	res = r.ok("monitor", "history")
	wants(t, res.out, "TIME", "gateway", "2.4 ms", "icmp", "1.1.1.1", "lost", "100%", "tcp")
	var samples []core.Sample
	r.jsonInto(&samples, "monitor", "history", "--anchor", "gateway")
	if len(samples) != 1 {
		t.Errorf("samples = %+v", samples)
	}

	res = r.ok("monitor", "baseline")
	wants(t, res.out, "gateway", "1.1.1.1")
	res = r.ok("monitor", "baseline", "--reset")
	wants(t, res.out, "Baseline reset for the current network")
	if got := r.mon.Resets(); len(got) != 1 || got[0] != "wifi:HomeNet" {
		t.Errorf("resets = %v", got)
	}

	res = r.ok("monitor", "pause")
	wants(t, res.out, "monitor paused")
	if !r.mon.Status().Paused {
		t.Error("monitor should be paused")
	}
	res = r.ok("monitor")
	wants(t, res.out, "paused")
	res = r.ok("monitor", "resume")
	wants(t, res.out, "monitor resumed")
}

func TestSpeed(t *testing.T) {
	r := newRig(t)
	res := r.ok("speed", "--quick")
	// Non-TTY: one line per phase, then the summary.
	wants(t, res.out, "latency", "download", "120.5 Mbps", "upload", "18.3 Mbps", "[", "Speed test via cloudflare quick",
		"Download  240.1 Mbps", "Upload    18.3 Mbps", "Latency   12 ms  jitter 1.1 ms", "44.0 MB", "Network   wifi:HomeNet")
	if r.speed.Runs() != 1 {
		t.Errorf("runs = %d", r.speed.Runs())
	}
	var sr core.SpeedResult
	r.jsonInto(&sr, "speed", "--provider", "iperf3", "--server", "host:5201")
	if sr.DownloadMbps != 240.1 || sr.Provider != "iperf3" || sr.Server != "host:5201" || sr.Quick {
		t.Errorf("speed json = %+v", sr)
	}
	res = r.ok("speed", "history")
	wants(t, res.out, "TIME", "wifi:HomeNet", "240.1 Mbps", "cloudflare (quick)", "iperf3")
	var hist []core.SpeedResult
	r.jsonInto(&hist, "speed", "history", "--limit", "1")
	if len(hist) != 1 {
		t.Errorf("history = %d", len(hist))
	}

	r.speed.Err = core.Errorf(core.KindUnavailable, "check the connection", "speed: cloudflare unreachable")
	res = r.run("speed")
	if res.code != ExitError {
		t.Errorf("exit = %d", res.code)
	}
	wants(t, res.err, "error: speed: cloudflare unreachable", "hint: check the connection")
}

func TestEventsHistoryAndFollow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res := r.ok("events")
	wants(t, res.out, "no events yet")

	// vpn up/down produce stored events.
	r.ok("vpn", "up", "wg-home", "-q")
	r.ok("vpn", "down", "wg-home", "-q")
	res = r.ok("events")
	wants(t, res.out, "TIME", "vpn-up", "vpn-down", "wg-home connected", "wifi:HomeNet")
	var evs []core.Event
	r.jsonInto(&evs, "events", "--limit", "1")
	if len(evs) != 1 || evs[0].Type != core.EventVPNDown {
		t.Errorf("events json = %+v", evs)
	}

	// Follow with a limit: two change hints, one JSON line each.
	type followResult struct {
		res result
	}
	done := make(chan followResult, 1)
	go func() {
		done <- followResult{r.runIn(ctx, "", "events", "--follow", "--changes", "--limit", "2", "--json")}
	}()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	var fr followResult
	deadline := time.After(5 * time.Second)
wait:
	for {
		select {
		case fr = <-done:
			break wait
		case <-tick.C:
			_ = r.nm.Scan(ctx, "") // each scan is one wifi change
		case <-deadline:
			t.Fatal("follow did not stop after 2 items")
		}
	}
	if fr.res.code != 0 {
		t.Fatalf("follow: %d %s", fr.res.code, fr.res.err)
	}
	lines := strings.Split(strings.TrimSpace(fr.res.out), "\n")
	if len(lines) != 2 {
		t.Fatalf("follow lines = %d: %q", len(lines), fr.res.out)
	}
	for _, l := range lines {
		var item api.StreamItem
		if err := json.Unmarshal([]byte(l), &item); err != nil || item.Change == nil || item.Change.Kind != core.ChangeWifi {
			t.Errorf("item %q: %v %+v", l, err, item)
		}
	}

	// Follow until cancelled: exit 0, no error text.
	cctx, cancel := context.WithCancel(ctx)
	done2 := make(chan result, 1)
	go func() { done2 <- r.runIn(cctx, "", "events", "--follow") }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case res := <-done2:
		if res.code != 0 || res.err != "" {
			t.Errorf("cancelled follow: %d %q", res.code, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not stop on cancel")
	}
}

func TestDiag(t *testing.T) {
	r := newRig(t)
	res := r.ok("diag", "devices")
	wants(t, res.out, "192.168.1.1", "router.lan (gateway)", "laptop (this host)", "printer.lan", "stale", "192.168.1.77")
	res = r.ok("diag", "devices", "--no-sweep")
	if strings.Contains(res.out, "192.168.1.77") {
		t.Error("--no-sweep should not include the swept host")
	}
	res = r.ok("diag", "ports")
	wants(t, res.out, "PROTO", "tcp", "22", "sshd", "811", "root", "udp", "5353", "avahi-daemon", "* = every interface")
	res = r.ok("diag", "routes")
	wants(t, res.out, "default", "192.168.1.1", "wlan0", "600", "dhcp", "192.168.1.0/24", "fe80::/64")
	res = r.ok("diag", "dns", "example.com")
	wants(t, res.out, "example.com A via 192.168.1.1 in 8ms", "93.184.216.34")
	res = r.ok("diag", "dns", "example.com", "--type", "AAAA", "--server", "1.1.1.1")
	wants(t, res.out, "AAAA via 1.1.1.1", "2606:2800")
	var ans core.DNSAnswer
	r.jsonInto(&ans, "diag", "dns", "example.com")
	if ans.Name != "example.com" || len(ans.Answers) != 1 {
		t.Errorf("dns json = %+v", ans)
	}
	res = r.ok("diag", "public-ip")
	wants(t, res.out, "203.0.113.7", "Location  DE", "Colo      FRA", "cloudflare-trace")
	res = r.ok("diag", "infra")
	wants(t, res.out, "docker0", "docker", "172.17.0.1/16", "└─ veth1a2b3c", "veth", "up")
	var nets []core.InfraNetwork
	r.jsonInto(&nets, "diag", "infra")
	if len(nets) != 1 || nets[0].Owner != "docker" {
		t.Errorf("infra json = %+v", nets)
	}
}

func TestConfig(t *testing.T) {
	r := newRig(t)
	res := r.ok("config")
	wants(t, res.out, "[monitor]", "monitor.interval = 30s", "[notify]", "notify.degraded = false", "speed.provider = cloudflare", `tailscale.socket = ""`)

	res = r.ok("config", "set", "notify.degraded", "true")
	wants(t, res.out, "notify.degraded = true")
	res = r.ok("config", "get", "notify.degraded")
	if strings.TrimSpace(res.out) != "true" {
		t.Errorf("get = %q", res.out)
	}
	var cfg config.Config
	r.jsonInto(&cfg, "config")
	if !cfg.Notify.Degraded {
		t.Error("config json should show the change")
	}
	if got := r.d.Config(); !got.Notify.Degraded {
		t.Error("daemon config should have changed")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "config.toml")); err != nil {
		t.Errorf("config not persisted: %v", err)
	}

	res = r.run("config", "set", "monitor.interval", "1ms")
	if res.code != ExitError || !strings.Contains(res.err, "at least 1s") {
		t.Errorf("invalid set: %d %q", res.code, res.err)
	}
	res = r.run("config", "get", "nope.key")
	if res.code != ExitError || !strings.Contains(res.err, "hint: run `bnm config keys`") {
		t.Errorf("unknown key: %d %q", res.code, res.err)
	}

	res = r.ok("config", "mute", "HomeNet")
	wants(t, res.out, "Muted wifi:HomeNet")
	res = r.ok("config", "get", "notify.muted_networks")
	if strings.TrimSpace(res.out) != "wifi:HomeNet" {
		t.Errorf("muted = %q", res.out)
	}
	res = r.ok("config", "mute", "ethernet:abc")
	res = r.ok("config", "unmute", "wifi:HomeNet")
	wants(t, res.out, "Unmuted wifi:HomeNet")
	res = r.ok("config", "get", "notify.muted_networks")
	if strings.TrimSpace(res.out) != "ethernet:abc" {
		t.Errorf("muted after unmute = %q", res.out)
	}
	res = r.run("config", "mute", "nonsense")
	if res.code != ExitError {
		t.Errorf("mute non-key: %d", res.code)
	}

	res = r.ok("config", "keys")
	wants(t, res.out, "monitor.interval", "(default 30s)")
	res = r.ok("config", "path")
	wants(t, res.out, filepath.Join(r.dir, "cfg", "bnm", "config.toml"))
	res = r.ok("notify", "test")
	wants(t, res.out, "sent a test notification")
}

func TestExitCodesAndUsage(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"bogus"}, ExitUsage, `unknown command "bogus"`},
		{[]string{"wifi", "bogus"}, ExitUsage, `unknown command "bogus" for "bnm wifi"`},
		{[]string{"wifi", "connect"}, ExitUsage, "takes 1 argument(s), got 0"},
		{[]string{"status", "extra"}, ExitUsage, "takes no arguments"},
		{[]string{"--bogus", "status"}, ExitUsage, "unknown flag: --bogus"},
		{[]string{"wifi", "connect", "x", "--password", "p", "--ask"}, ExitUsage, "mutually exclusive"},
		{[]string{"vpn", "ts", "dns", "sideways"}, ExitUsage, "want on or off"},
	}
	for _, c := range cases {
		res := r.run(c.args...)
		if res.code != c.code || !strings.Contains(res.err, c.err) {
			t.Errorf("bnm %s: code %d err %q; want %d %q", strings.Join(c.args, " "), res.code, res.err, c.code, c.err)
		}
		if c.code == ExitUsage && !strings.Contains(res.err, "usage: bnm") {
			t.Errorf("bnm %s: usage line missing: %q", strings.Join(c.args, " "), res.err)
		}
	}

	// Group commands print their help and exit 0.
	res := r.run("wifi")
	if res.code != 0 || !strings.Contains(res.out, "Available Commands") {
		t.Errorf("bare group: %d %q", res.code, res.out)
	}
	res = r.run("--help")
	if res.code != 0 {
		t.Errorf("--help: %d", res.code)
	}
	for _, name := range []string{"status", "wifi", "wired", "profile", "vpn", "monitor", "speed", "events", "diag", "config", "notify", "daemon", "version", "tui"} {
		if !strings.Contains(res.out, "\n  "+name+" ") {
			t.Errorf("help lacks %s", name)
		}
	}
	// Bare bnm without a TUI is the help.
	res = r.run()
	if res.code != 0 || !strings.Contains(res.out, "Usage:") {
		t.Errorf("bare bnm: %d %q", res.code, res.out)
	}
	res = r.run("tui")
	if res.code != ExitError || !strings.Contains(res.err, "not built into this binary") {
		t.Errorf("tui without hook: %d %q", res.code, res.err)
	}

	// Daemon unreachable is 3.
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"--socket", filepath.Join(r.dir, "none.sock"), "--no-autostart", "status"}, strings.NewReader(""), &out, &errb)
	if code != ExitUnreachable || !strings.Contains(errb.String(), "bnmd is not running") || !strings.Contains(errb.String(), "hint: run `bnm daemon start`") {
		t.Errorf("unreachable: %d %q", code, errb.String())
	}

	// Permission is 4.
	r.nm.Fail("SetWifiEnabled", core.Errorf(core.KindPermission, "allow org.freedesktop.NetworkManager.enable-disable-wifi in polkit", "nm: not authorized"))
	res = r.run("wifi", "off")
	if res.code != ExitPermission || !strings.Contains(res.err, "hint: allow org.freedesktop") {
		t.Errorf("permission: %d %q", res.code, res.err)
	}
	r.nm.Fail("SetWifiEnabled", nil)
}

func TestColour(t *testing.T) {
	r := newRig(t)
	res := r.ok("vpn", "list")
	if hasANSI(res.out) {
		t.Error("no colour without a TTY")
	}
	t.Setenv("CLICOLOR_FORCE", "1")
	res = r.ok("vpn", "list")
	if !hasANSI(res.out) {
		t.Error("CLICOLOR_FORCE should colour the output")
	}
	res = r.ok("vpn", "list", "--no-color")
	if hasANSI(res.out) {
		t.Error("--no-color must win")
	}
	t.Setenv("NO_COLOR", "1")
	res = r.ok("vpn", "list")
	if hasANSI(res.out) {
		t.Error("NO_COLOR must win over CLICOLOR_FORCE")
	}
	t.Setenv("NO_COLOR", "")
	res = r.ok("wifi", "list")
	if !hasANSI(res.out) || !strings.Contains(res.out, "▂▄▆█") {
		t.Errorf("coloured bars expected: %q", res.out)
	}
}

func TestTUIHook(t *testing.T) {
	r := newRig(t)
	calls := 0
	RunTUI = func(ctx context.Context, c *client.Client) error {
		calls++
		st, err := c.Status(ctx)
		if err != nil || st.Version != "test-1" {
			t.Errorf("hook client: %+v %v", st, err)
		}
		return nil
	}
	t.Cleanup(func() { RunTUI = nil })
	if res := r.run(); res.code != 0 || res.out != "" {
		t.Errorf("bare bnm with hook: %d %q", res.code, res.out)
	}
	if res := r.run("tui"); res.code != 0 {
		t.Errorf("bnm tui with hook: %d %s", res.code, res.err)
	}
	if calls != 2 {
		t.Errorf("hook calls = %d", calls)
	}
	RunTUI = func(context.Context, *client.Client) error { return errors.New("boom") }
	if res := r.run("tui"); res.code != ExitError || !strings.Contains(res.err, "error: boom") {
		t.Errorf("hook error: %d %q", res.code, res.err)
	}
}

func TestVersion(t *testing.T) {
	r := newRig(t)
	res := r.ok("version")
	wants(t, res.out, "bnm dev (api v1)", "bnmd test-1 (api v1)")
	var v struct {
		Version       string `json:"version"`
		DaemonVersion string `json:"daemon_version"`
	}
	r.jsonInto(&v, "version")
	if v.DaemonVersion != "test-1" {
		t.Errorf("version json = %+v", v)
	}
}

func TestDaemonCommands(t *testing.T) {
	r := newRig(t)
	t.Setenv("PATH", "") // no systemctl: the file paths are exercised
	res := r.run("daemon", "status")
	if res.code != 0 {
		t.Fatalf("status: %d %s", res.code, res.err)
	}
	wants(t, res.out, "bnmd test-1 running", "Socket   "+r.socket, "API      v1", "systemctl not found")
	var info daemonInfo
	r.jsonInto(&info, "daemon", "status")
	if !info.Running || info.PID != os.Getpid() || info.Version != "test-1" {
		t.Errorf("daemon json = %+v", info)
	}

	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"--socket", filepath.Join(r.dir, "none.sock"), "daemon", "status"}, strings.NewReader(""), &out, &errb)
	if code != ExitUnreachable || !strings.Contains(out.String(), "bnmd not running") || errb.Len() != 0 {
		t.Errorf("status when down: %d %q %q", code, out.String(), errb.String())
	}

	res = r.ok("daemon", "start")
	wants(t, res.out, "already running")

	// install --user-unit writes the unit and nothing else.
	t.Setenv("BNM_DAEMON", "/opt/bnm/bnmd")
	res = r.ok("daemon", "install", "--user-unit")
	unit := filepath.Join(r.dir, "cfg", "systemd", "user", "bnmd.service")
	wants(t, res.out, "wrote "+unit, "ExecStart=/opt/bnm/bnmd", "enable it with")
	data, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "[Unit]", "ExecStart=/opt/bnm/bnmd", "WantedBy=default.target", "Restart=on-failure")
	res = r.run("daemon", "install")
	if res.code != ExitError || !strings.Contains(res.err, "systemctl not found") {
		t.Errorf("install without systemctl: %d %q", res.code, res.err)
	}
	res = r.ok("daemon", "uninstall")
	wants(t, res.out, "removed "+unit)
	if _, err := os.Stat(unit); err == nil {
		t.Error("unit should be gone")
	}
	res = r.ok("daemon", "uninstall")
	wants(t, res.out, "nothing to do")

	// logs: the file next to the state dir.
	logDir := filepath.Join(r.dir, "bnm")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "bnmd.log")
	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = r.ok("daemon", "logs", "-n", "2")
	if res.out != "two\nthree\n" {
		t.Errorf("logs = %q", res.out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan result, 1)
	go func() { done <- r.runIn(ctx, "", "daemon", "logs", "-n", "1", "-f") }()
	time.Sleep(100 * time.Millisecond)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = io.WriteString(f, "four\n")
	f.Close()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case fr := <-done:
		if fr.code != 0 || fr.out != "three\nfour\n" {
			t.Errorf("logs -f = %d %q", fr.code, fr.out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("logs -f did not stop")
	}
	os.Remove(logPath)
	res = r.run("daemon", "logs")
	if res.code != ExitError || !strings.Contains(res.err, "open log") {
		t.Errorf("missing log: %d %q", res.code, res.err)
	}
}

func TestHelpers(t *testing.T) {
	tests := []struct {
		got, want string
	}{
		{shortDuration(5 * time.Second), "5s"},
		{shortDuration(125 * time.Second), "2m05s"},
		{shortDuration(3*time.Hour + 7*time.Minute), "3h07m"},
		{shortDuration(49 * time.Hour), "2d1h"},
		{ms(-1), "-"},
		{ms(2.34), "2.3 ms"},
		{ms(120.6), "121 ms"},
		{pct(0.105), "10%"},
		{bytesHuman(999), "999 B"},
		{bytesHuman(44_000_000), "44.0 MB"},
		{mbps(0), "-"},
		{mbps(7.123), "7.12 Mbps"},
		{mbps(240.14), "240.1 Mbps"},
		{truncate("abcdefgh", 5), "abcd…"},
		{truncate("abc", 5), "abc"},
		{securityLabel(core.SecSAE), "WPA3"},
		{securityLabel(""), "-"},
		{progressBar(50, 4), "[██░░]"},
		{shortUUID(fake.HomeUUID), "11111111"},
		{ago(time.Now().Add(-90 * time.Second)), "1m ago"},
		{ago(time.Now().Add(-49 * time.Hour)), "2d ago"},
	}
	for i, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("case %d: got %q want %q", i, tt.got, tt.want)
		}
	}
	u := newUI(io.Discard, true)
	if u.bars(20) != "▂   " || u.bars(60) != "▂▄▆ " || u.bars(100) != "▂▄▆█" || u.bars(0) != "    " {
		t.Errorf("bars = %q %q %q", u.bars(20), u.bars(60), u.bars(100))
	}
	if u.dot("green") != "●" || u.dot("") != "○" {
		t.Errorf("dots = %q %q", u.dot("green"), u.dot(""))
	}
}
