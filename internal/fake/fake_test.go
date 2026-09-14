package fake

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

var (
	_ core.NetworkManager   = (*NM)(nil)
	_ core.VPNAdapter       = (*VPNAdapter)(nil)
	_ core.TailscaleControl = (*VPNAdapter)(nil)
	_ core.Store            = (*Store)(nil)
	_ core.Notifier         = (*Notifier)(nil)
	_ core.SpeedTester      = (*SpeedTester)(nil)
)

func TestSeededWorld(t *testing.T) {
	ctx := context.Background()
	n := NewNM()
	st, err := n.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Primary == nil || st.Primary.ProfileName != HomeSSID || st.NetworkKey != "wifi:"+HomeSSID || st.Connectivity != core.ConnFull {
		t.Fatalf("status = %+v", st)
	}
	devs, _ := n.Devices(ctx)
	if len(devs) != 2 || devs[0].Kind != core.DeviceWifi || devs[1].Kind != core.DeviceEthernet {
		t.Fatalf("devices = %+v", devs)
	}
	nets, _ := n.WifiNetworks(ctx, "")
	if len(nets) != 4 {
		t.Fatalf("wifi = %d", len(nets))
	}
	active := 0
	for _, w := range nets {
		if w.Active {
			active++
		}
	}
	if active != 1 {
		t.Errorf("active SSIDs = %d", active)
	}
	profiles, _ := n.Profiles(ctx)
	if len(profiles) != 3 || !profiles[0].Active || profiles[1].Active {
		t.Fatalf("profiles = %+v", profiles)
	}
	if _, err := n.WifiNetworks(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown device: %v", err)
	}
}

func drain(ch <-chan core.Change, wait time.Duration) []core.Change {
	var out []core.Change
	deadline := time.After(wait)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
		case <-deadline:
			return out
		}
	}
}

func hasKind(cs []core.Change, k core.ChangeKind) bool {
	for _, c := range cs {
		if c.Kind == k {
			return true
		}
	}
	return false
}

func TestWatchAndMutators(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := NewNM()
	ch, err := n.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	n.SetConnectivity(core.ConnPortal)
	cs := drain(ch, 50*time.Millisecond)
	if !hasKind(cs, core.ChangeStatus) {
		t.Errorf("SetConnectivity: %+v", cs)
	}
	st, _ := n.Status(ctx)
	if st.Connectivity != core.ConnPortal {
		t.Errorf("connectivity = %s", st.Connectivity)
	}

	n.DisconnectAll()
	cs = drain(ch, 50*time.Millisecond)
	if !hasKind(cs, core.ChangeActive) {
		t.Errorf("DisconnectAll: %+v", cs)
	}
	st, _ = n.Status(ctx)
	if st.Primary != nil {
		t.Errorf("primary should be gone")
	}
	devs, _ := n.Devices(ctx)
	if devs[0].State != core.DeviceDisconnected || devs[0].ActiveUUID != "" {
		t.Errorf("device not reset: %+v", devs[0])
	}

	n.AddAP(WifiDevice, core.WifiNetwork{SSID: "New", Security: core.SecOpen, Strength: 70})
	if cs = drain(ch, 50*time.Millisecond); !hasKind(cs, core.ChangeWifi) {
		t.Errorf("AddAP: %+v", cs)
	}
	nets, _ := n.WifiNetworks(ctx, WifiDevice)
	if len(nets) != 5 {
		t.Errorf("wifi after AddAP = %d", len(nets))
	}
	n.RemoveAP(WifiDevice, "New")
	drain(ch, 20*time.Millisecond)
	nets, _ = n.WifiNetworks(ctx, WifiDevice)
	if len(nets) != 4 {
		t.Errorf("wifi after RemoveAP = %d", len(nets))
	}

	cancel()
	if _, ok := <-ch; ok {
		// drain until closed
		for range ch {
		}
	}
}

func TestConnectWifi(t *testing.T) {
	ctx := context.Background()
	n := NewNM()
	tests := []struct {
		name string
		req  core.ConnectWifiRequest
		kind core.ErrorKind
	}{
		{"no ssid", core.ConnectWifiRequest{}, core.KindInvalid},
		{"unknown device", core.ConnectWifiRequest{SSID: CafeSSID, Device: "wlan9"}, core.KindNotFound},
		{"not in range", core.ConnectWifiRequest{SSID: "Ghost"}, core.KindNotFound},
		{"needs password", core.ConnectWifiRequest{SSID: NeighbourSSD}, core.KindInvalid},
		{"open ok", core.ConnectWifiRequest{SSID: CafeSSID}, ""},
		{"psk ok", core.ConnectWifiRequest{SSID: NeighbourSSD, Password: "hunter22"}, ""},
		{"known ok", core.ConnectWifiRequest{SSID: OfficeSSID}, ""},
		{"hidden ok", core.ConnectWifiRequest{SSID: "Secret", Hidden: true, Password: "pw"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := n.ConnectWifi(ctx, tt.req)
			if tt.kind == "" {
				if err != nil {
					t.Fatal(err)
				}
				st, _ := n.Status(ctx)
				if st.Primary == nil || st.Primary.ProfileName != tt.req.SSID || st.NetworkKey != "wifi:"+tt.req.SSID {
					t.Errorf("primary = %+v", st.Primary)
				}
				if len(st.Primary.IPv4) == 0 || st.Primary.Gateway4 == "" {
					t.Errorf("primary has no address: %+v", st.Primary)
				}
				return
			}
			if core.KindOf(err) != tt.kind {
				t.Errorf("err = %v (kind %s), want %s", err, core.KindOf(err), tt.kind)
			}
		})
	}
	profiles, _ := n.Profiles(ctx)
	if len(profiles) != 6 {
		t.Errorf("profiles = %d, want 6 (3 seeded + cafe + neighbour + hidden)", len(profiles))
	}
	actives, _ := n.ActiveConnections(ctx)
	if len(actives) != 1 {
		t.Errorf("one wifi device carries one connection, got %d", len(actives))
	}
}

func TestProfilesLifecycle(t *testing.T) {
	ctx := context.Background()
	n := NewNM()
	if err := n.Activate(ctx, WiredUUID, ""); err != nil {
		t.Fatal(err)
	}
	st, _ := n.Status(ctx)
	if st.Primary.ProfileUUID != WiredUUID {
		t.Errorf("wired should be primary now: %+v", st.Primary)
	}
	actives, _ := n.ActiveConnections(ctx)
	if len(actives) != 2 {
		t.Errorf("wifi + wired active, got %d", len(actives))
	}
	if err := n.Deactivate(ctx, WiredUUID); err != nil {
		t.Fatal(err)
	}
	if err := n.Deactivate(ctx, WiredUUID); core.KindOf(err) != core.KindConflict {
		t.Errorf("double deactivate: %v", err)
	}
	if err := n.SetAutoconnect(ctx, HomeUUID, false); err != nil {
		t.Fatal(err)
	}
	p, _ := n.Profile(ctx, HomeUUID)
	if p.Autoconnect || p.VersionID != 2 {
		t.Errorf("autoconnect: %+v", p)
	}
	if err := n.UpdateIPConfig(ctx, HomeUUID, &core.IPConfig{Method: core.IPManual, Addresses: []string{"10.0.0.5/24"}}, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = n.Profile(ctx, HomeUUID)
	if p.IPv4.Method != core.IPManual || p.IPv6.Method != core.IPAuto {
		t.Errorf("ip config: %+v", p)
	}
	if err := n.SetProfilePermissions(ctx, HomeUUID, true); err != nil {
		t.Fatal(err)
	}
	uuid, err := n.AddWireGuard(ctx, core.WireGuardSpec{Name: "wg0", InterfaceName: "wg0", PrivateKey: "k", Addresses: []string{"10.8.0.2/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Activate(ctx, uuid, ""); err != nil {
		t.Fatal(err)
	}
	st, _ = n.Status(ctx)
	if st.Primary == nil || st.Primary.ProfileUUID != HomeUUID {
		t.Errorf("wireguard must not become primary: %+v", st.Primary)
	}
	ov, err := n.ImportVPN(ctx, "openvpn", "/tmp/office.ovpn")
	if err != nil {
		t.Fatal(err)
	}
	p, _ = n.Profile(ctx, ov)
	if p.Type != core.ProfileVPN || p.Name != "office" || !strings.HasSuffix(p.VPNServiceType, "openvpn") {
		t.Errorf("imported: %+v", p)
	}
	if err := n.Forget(ctx, HomeUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Profile(ctx, HomeUUID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("forget: %v", err)
	}
	nets, _ := n.WifiNetworks(ctx, "")
	for _, w := range nets {
		if w.SSID == HomeSSID && (w.Known || w.Active) {
			t.Errorf("HomeNet should be unknown after forget: %+v", w)
		}
	}
	if err := n.SetWifiEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := n.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: CafeSSID}); core.KindOf(err) != core.KindUnavailable {
		t.Errorf("connect with wifi off: %v", err)
	}
}

func TestFailInjection(t *testing.T) {
	ctx := context.Background()
	n := NewNM()
	want := core.Errorf(core.KindPermission, "run: sudo usermod -aG netdev $USER", "nm: not authorised")
	n.Fail("Scan", want)
	if err := n.Scan(ctx, ""); !errors.Is(err, core.ErrPermission) || core.HintOf(err) == "" {
		t.Errorf("Scan = %v", err)
	}
	n.Fail("Scan", nil)
	if err := n.Scan(ctx, ""); err != nil {
		t.Errorf("Scan after clear = %v", err)
	}
}

func TestRoamKeepsProfileChangesPath(t *testing.T) {
	ctx := context.Background()
	n := NewNM()
	before, _ := n.Status(ctx)
	n.Roam()
	after, _ := n.Status(ctx)
	if after.Primary == nil || after.Primary.ProfileUUID != before.Primary.ProfileUUID || after.Primary.Path == before.Primary.Path {
		t.Errorf("roam: before %+v after %+v", before.Primary, after.Primary)
	}
}

func TestVPNAdapter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ts := NewTailscale()
	reg := NewVPNRegistry(ts, NewWireGuard(), NewNMVPN())
	ch, err := reg.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vs, _ := reg.List(ctx)
	if len(vs) != 3 {
		t.Fatalf("vpns = %d", len(vs))
	}
	if err := reg.Connect(ctx, "tailscale"); err != nil {
		t.Fatal(err)
	}
	if cs := drain(ch, 50*time.Millisecond); !hasKind(cs, core.ChangeVPN) {
		t.Errorf("connect change: %+v", cs)
	}
	vs, _ = ts.List(ctx)
	if vs[0].State != core.VPNConnected || vs[0].Tailscale.BackendState != "Running" {
		t.Errorf("state = %+v", vs[0])
	}
	if err := reg.Connect(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	tc := reg.Tailscale()
	if tc == nil {
		t.Fatal("no tailscale control")
	}
	if err := tc.SetExitNode(ctx, "phone", false); core.KindOf(err) != core.KindInvalid {
		t.Errorf("phone is not an exit node: %v", err)
	}
	if err := tc.SetExitNode(ctx, "homeserver", true); err != nil {
		t.Fatal(err)
	}
	vs, _ = ts.List(ctx)
	if !vs[0].Tailscale.ExitNodeOn || vs[0].Tailscale.ExitNodeName != "homeserver" || !vs[0].Tailscale.AllowLAN {
		t.Errorf("exit node: %+v", vs[0].Tailscale)
	}
	if err := tc.UseExitNode(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetAcceptDNS(ctx, false); err != nil {
		t.Fatal(err)
	}
	url, err := tc.Login(ctx)
	if err != nil || url == "" {
		t.Fatalf("login: %s %v", url, err)
	}
	if err := tc.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if NewVPNRegistry(NewWireGuard()).Tailscale() != nil {
		t.Errorf("registry without tailscale must return nil")
	}
	wg := NewWireGuard()
	if _, err := wg.Login(ctx); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("wireguard login: %v", err)
	}
}

func TestVPNLatency(t *testing.T) {
	ctx := context.Background()
	ts := NewTailscale()
	ts.Latency = 10 * time.Millisecond
	if err := ts.Connect(ctx, "tailscale"); err != nil {
		t.Fatal(err)
	}
	vs, _ := ts.List(ctx)
	if vs[0].State != core.VPNConnecting {
		t.Errorf("state = %s, want connecting", vs[0].State)
	}
	time.Sleep(30 * time.Millisecond)
	vs, _ = ts.List(ctx)
	if vs[0].State != core.VPNConnected {
		t.Errorf("state = %s, want connected", vs[0].State)
	}
}

func TestStore(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	for i := 0; i < 5; i++ {
		_ = s.AddSample(ctx, core.Sample{Time: time.Now(), NetworkKey: "wifi:A", Anchor: "gateway", RTTms: float64(i)})
		_ = s.AddEvent(ctx, core.Event{Type: core.EventConnected, Title: string(rune('a' + i))})
	}
	_ = s.AddSample(ctx, core.Sample{Time: time.Now(), NetworkKey: "wifi:B", Anchor: "gateway"})
	got, _ := s.Samples(ctx, "wifi:A", "gateway", 2)
	if len(got) != 2 || got[1].RTTms != 4 {
		t.Errorf("samples = %+v", got)
	}
	ev, _ := s.Events(ctx, 3)
	if len(ev) != 3 || ev[2].Title != "e" {
		t.Errorf("events = %+v", ev)
	}
	_ = s.PutBaseline(ctx, core.Baseline{NetworkKey: "wifi:A", Anchor: "gateway"})
	_ = s.PutBaseline(ctx, core.Baseline{NetworkKey: "wifi:A", Anchor: "1.1.1.1"})
	bs, _ := s.Baselines(ctx, "wifi:A")
	if len(bs) != 2 {
		t.Errorf("baselines = %d", len(bs))
	}
	_ = s.DeleteBaselines(ctx, "wifi:A")
	if bs, _ = s.Baselines(ctx, "wifi:A"); len(bs) != 0 {
		t.Errorf("baselines after delete = %d", len(bs))
	}
	_ = s.AddSpeedResult(ctx, core.SpeedResult{NetworkKey: "wifi:A", DownloadMbps: 1})
	sr, _ := s.SpeedResults(ctx, "", 0)
	if len(sr) != 1 {
		t.Errorf("speed = %d", len(sr))
	}
	s.Retention = time.Nanosecond
	time.Sleep(time.Millisecond)
	_ = s.Prune(ctx)
	if got, _ = s.Samples(ctx, "", "", 0); len(got) != 0 {
		t.Errorf("prune left %d", len(got))
	}
	_ = s.Close()
	if !s.Closed() {
		t.Error("closed")
	}
}

func TestNotifierMonitorSpeedDiag(t *testing.T) {
	ctx := context.Background()
	nt := NewNotifier()
	_ = nt.Notify(ctx, core.Event{Type: core.EventConnected})
	if len(nt.Events()) != 1 {
		t.Error("notifier did not record")
	}
	select {
	case <-nt.C():
	default:
		t.Error("notifier chan empty")
	}

	m := NewMonitor()
	m.SetNetwork("wifi:A", "192.168.1.1")
	if st := m.Status(); st.NetworkKey != "wifi:A" || st.State != core.BaselineLearning || len(st.Anchors) != 2 {
		t.Errorf("monitor status = %+v", st)
	}
	m.Pause()
	if !m.Status().Paused {
		t.Error("pause")
	}
	m.Resume()
	if err := m.ResetBaseline(ctx, ""); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("reset empty: %v", err)
	}
	_ = m.ResetBaseline(ctx, "wifi:A")
	if m.Resets()[0] != "wifi:A" || m.Networks()[0].Gateway != "192.168.1.1" {
		t.Error("monitor records")
	}
	m.Emit(core.Event{Type: core.EventDegraded})
	select {
	case e := <-m.Events():
		if e.Type != core.EventDegraded || e.Time.IsZero() {
			t.Errorf("event = %+v", e)
		}
	default:
		t.Error("no monitor event")
	}

	sp := NewSpeedTester()
	var phases []string
	r, err := sp.Run(ctx, core.SpeedOptions{Quick: true}, func(p core.SpeedProgress) { phases = append(phases, p.Phase) })
	if err != nil || r.DownloadMbps == 0 || !r.Quick || r.Provider != "cloudflare" || len(phases) != 5 {
		t.Errorf("speed = %+v %v %v", r, err, phases)
	}
	sp.Err = errors.New("boom")
	if _, err := sp.Run(ctx, core.SpeedOptions{}, nil); err == nil {
		t.Error("speed err")
	}

	d := NewDiag()
	if h, _ := d.LANHosts(ctx, "", true); len(h) != 4 {
		t.Errorf("lan = %d", len(h))
	}
	if p, _ := d.ListeningPorts(ctx); len(p) == 0 {
		t.Error("ports")
	}
	if r, _ := d.Routes(ctx); len(r) == 0 {
		t.Error("routes")
	}
	if _, err := d.DNSLookup(ctx, "", "", ""); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("dns empty: %v", err)
	}
	if a, _ := d.DNSLookup(ctx, "example.com", "", "aaaa"); a.Type != "AAAA" || len(a.Answers) != 1 {
		t.Errorf("dns = %+v", a)
	}
	if ip, _ := d.PublicIP(ctx); ip.IP == "" {
		t.Error("public ip")
	}
	if inf, _ := d.Infra(ctx); len(inf) != 1 || inf[0].Owner != "docker" {
		t.Error("infra")
	}

	n := NewNM()
	imp := &WireGuardImporter{NM: n}
	if _, err := imp.Import(ctx, "wg1", strings.NewReader("[Interface]\nAddress = 10.0.0.2/24\n")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("import without key: %v", err)
	}
	uuid, err := imp.Import(ctx, "wg1", strings.NewReader("[Interface]\nPrivateKey = abc=\nAddress = 10.0.0.2/24, fd00::2/64\nDNS = 1.1.1.1\n[Peer]\nPublicKey = def=\nEndpoint = vpn:51820\nAllowedIPs = 0.0.0.0/0\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := n.Profile(ctx, uuid)
	if p.Type != core.ProfileWireGuard || len(p.IPv4.Addresses) != 2 {
		t.Errorf("imported wg: %+v", p)
	}
}

func TestSecretBrokerAndPromptingNM(t *testing.T) {
	ctx := context.Background()
	b := NewSecretBroker()
	b.Timeout = 200 * time.Millisecond
	var mu sync.Mutex
	var needed []core.SecretRequest
	var resolved [][2]string
	b.SetSecretHandler(func(r core.SecretRequest) { mu.Lock(); needed = append(needed, r); mu.Unlock() })
	b.SetSecretResolvedHandler(func(id string, o core.SecretOutcome) {
		mu.Lock()
		resolved = append(resolved, [2]string{id, string(o)})
		mu.Unlock()
	})

	// Raise fills in the id and times, publishes, and the answer flows back.
	ch := b.Raise(core.SecretRequest{ConnectionName: "Cafe", SSID: "Cafe"})
	mu.Lock()
	if len(needed) != 1 || needed[0].ID == "" || needed[0].ExpiresAt.IsZero() || len(needed[0].Fields) != 1 {
		t.Fatalf("needed = %+v", needed)
	}
	id := needed[0].ID
	mu.Unlock()
	if p, _ := b.Pending(ctx); len(p) != 1 || p[0].ID != id {
		t.Fatalf("pending = %+v", p)
	}
	if err := b.Answer(ctx, id, core.SecretAnswer{Secrets: map[string]string{"nope": "x"}}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("answer without the requested key: %v", err)
	}
	if err := b.Answer(ctx, id, core.SecretAnswer{Secrets: map[string]string{"psk": "pw"}}); err != nil {
		t.Fatal(err)
	}
	if a := <-ch; a.Secrets["psk"] != "pw" {
		t.Fatalf("answer = %+v", a)
	}
	if err := b.Answer(ctx, id, core.SecretAnswer{Secrets: map[string]string{"psk": "pw"}}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second answer: %v", err)
	}
	if err := b.Cancel(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cancel missing: %v", err)
	}
	// Expiry closes the channel and reports timeout.
	ch = b.Raise(core.SecretRequest{ConnectionName: "Slow"})
	if _, ok := <-ch; ok {
		t.Fatal("expired request must close its channel")
	}
	mu.Lock()
	last := resolved[len(resolved)-1]
	mu.Unlock()
	if last[1] != string(core.SecretTimeout) {
		t.Fatalf("resolved = %v", resolved)
	}

	// A prompting NM asks for the password of a secured unknown network,
	// asks again (RequestNew) after a too-short answer, and connects.
	n := NewNM()
	n.UseSecrets(b)
	b.Timeout = 5 * time.Second
	b.SetSecretHandler(func(r core.SecretRequest) {
		go func() {
			pw := "hunter22"
			if !r.RequestNew {
				pw = "short"
			}
			_ = b.Answer(ctx, r.ID, core.SecretAnswer{Secrets: map[string]string{"psk": pw}})
		}()
		mu.Lock()
		needed = append(needed, r)
		mu.Unlock()
	})
	if err := n.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: NeighbourSSD}); err != nil {
		t.Fatalf("prompted connect: %v", err)
	}
	mu.Lock()
	rounds := needed[len(needed)-2:]
	mu.Unlock()
	if rounds[0].RequestNew || !rounds[1].RequestNew || rounds[1].SSID != NeighbourSSD {
		t.Fatalf("rounds = %+v", rounds)
	}
	st, _ := n.Status(ctx)
	if st.Primary == nil || st.Primary.ProfileName != NeighbourSSD {
		t.Fatalf("not connected: %+v", st.Primary)
	}
	// A cancelled prompt fails the connect with a hint.
	b.SetSecretHandler(func(r core.SecretRequest) { go func() { _ = b.Cancel(ctx, r.ID) }() })
	n.RemoveAP(WifiDevice, NeighbourSSD)
	n.AddAP(WifiDevice, core.WifiNetwork{SSID: "Other", Device: WifiDevice, Security: core.SecWPAPSK, Strength: 50})
	err := n.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: "Other"})
	if !errors.Is(err, core.ErrInvalid) || core.HintOf(err) == "" {
		t.Fatalf("cancelled prompt: %v", err)
	}
	// With a password given nothing is asked.
	mu.Lock()
	before := len(needed)
	mu.Unlock()
	if err := n.ConnectWifi(ctx, core.ConnectWifiRequest{SSID: "Other", Password: "longenough"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(needed) != before {
		t.Fatal("password given but prompted anyway")
	}
	mu.Unlock()

	// The nm-vpn adapter prompts for the VPN password too.
	ov := NewNMVPN()
	ov.Secrets = b
	b.SetSecretHandler(func(r core.SecretRequest) {
		if !r.VPN || r.Fields[0].Key != "password" {
			t.Errorf("vpn request = %+v", r)
		}
		go func() { _ = b.Answer(ctx, r.ID, core.SecretAnswer{Secrets: map[string]string{"password": "s3cret"}}) }()
	})
	vpns, _ := ov.List(ctx)
	if err := ov.Connect(ctx, vpns[0].ID); err != nil {
		t.Fatal(err)
	}
	if vpns, _ = ov.List(ctx); vpns[0].State != core.VPNConnected {
		t.Fatalf("vpn state = %s", vpns[0].State)
	}
}
