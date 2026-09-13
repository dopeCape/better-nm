package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

var t0 = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func wifiProfile(uuid, ssid string) core.Profile {
	return core.Profile{UUID: uuid, Name: ssid, Type: core.ProfileWifi, SSID: ssid}
}

func primary(uuid, name, ip, gw string, state core.ActiveState) *core.ActiveConnection {
	return &core.ActiveConnection{
		Path: "/ac/" + uuid, ProfileUUID: uuid, ProfileName: name, Type: core.ProfileWifi,
		Devices: []string{"wlan0"}, State: state, Default4: true, IPv4: []string{ip + "/24"}, Gateway4: gw,
	}
}

var (
	profiles = []core.Profile{wifiProfile("u-home", "HomeNet"), wifiProfile("u-cafe", "Cafe")}
	homeUp   = primary("u-home", "HomeNet", "192.168.1.42", "192.168.1.1", core.ActiveActivated)
	cafeUp   = primary("u-cafe", "Cafe", "10.1.0.9", "10.1.0.1", core.ActiveActivated)
)

func vw(p *core.ActiveConnection, conn core.Connectivity, vpns ...core.VPN) view {
	return view{Status: core.Status{Primary: p, Connectivity: conn}, Profiles: profiles, VPNs: vpns}
}

func types(evs []core.Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, string(e.Type))
	}
	return strings.Join(s, ",")
}

// step is one apply or flush in a scripted sequence.
type step struct {
	at    time.Duration // offset from t0
	view  *view         // nil = flush only
	want  string        // comma-joined event types
	check func(t *testing.T, evs []core.Event)
}

func runSteps(t *testing.T, d *deriver, steps []step) {
	t.Helper()
	for i, s := range steps {
		now := t0.Add(s.at)
		var evs []core.Event
		if s.view != nil {
			evs = d.apply(*s.view, now)
		} else {
			evs = d.flush(now)
		}
		if got := types(evs); got != s.want {
			t.Fatalf("step %d (at %v): events = %q, want %q", i, s.at, got, s.want)
		}
		if s.check != nil {
			s.check(t, evs)
		}
	}
}

func TestDeriveStartupIsSilent(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	up := core.VPN{ID: "tailscale", Name: "Tailscale", State: core.VPNConnected}
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull, up)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnFull, up)), want: ""},
	})
	if k, gw := d.monitorNetwork(); k != "wifi:HomeNet" || gw != "192.168.1.1" {
		t.Errorf("monitor network = %s %s", k, gw)
	}
}

func TestDeriveConnectFromNothing(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(nil, core.ConnNone)), want: ""},
		{at: time.Second, view: ptr(vw(primary("u-home", "HomeNet", "192.168.1.42", "192.168.1.1", core.ActiveActivating), core.ConnNone)), want: ""},
		{at: 2 * time.Second, view: ptr(vw(homeUp, core.ConnLimited)), want: "connected", check: func(t *testing.T, evs []core.Event) {
			e := evs[0]
			if e.Title != "Connected to HomeNet" || e.Body != "192.168.1.42 via wlan0" || e.NetworkKey != "wifi:HomeNet" || e.Urgency != "low" {
				t.Errorf("connected event = %+v", e)
			}
			if e.Data["ip"] != "192.168.1.42" || e.Data["gateway"] != "192.168.1.1" || e.Data["uuid"] != "u-home" {
				t.Errorf("data = %v", e.Data)
			}
		}},
		// connectivity goes full inside the grace: no no-internet event
		{at: 4 * time.Second, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: 20 * time.Second, want: ""},
	})
}

func TestDeriveDisconnectAfterDebounce(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(nil, core.ConnNone)), want: ""},
		{at: 3 * time.Second, want: ""},
		{at: 6 * time.Second, want: "disconnected", check: func(t *testing.T, evs []core.Event) {
			if evs[0].Title != "Disconnected from HomeNet" || evs[0].NetworkKey != "wifi:HomeNet" || evs[0].Urgency != "normal" {
				t.Errorf("disconnected = %+v", evs[0])
			}
		}},
	})
	if k, _ := d.monitorNetwork(); k != "" {
		t.Errorf("monitor network after disconnect = %q", k)
	}
	if dl, ok := d.nextDeadline(); ok {
		t.Errorf("no deadline expected, got %v", dl)
	}
}

func TestDeriveRoamWithinDebounceIsSilent(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	roamed := *homeUp
	roamed.Path = "/ac/u-home-2"
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(nil, core.ConnNone)), want: ""},
		{at: 3 * time.Second, view: ptr(vw(&roamed, core.ConnFull)), want: ""},
		{at: 30 * time.Second, want: ""},
	})
	if k, _ := d.monitorNetwork(); k != "wifi:HomeNet" {
		t.Errorf("monitor network after roam = %q", k)
	}
}

// A roam is "nothing happened": an outstanding no-internet must survive it so
// the later full connectivity says internet-restored (and a still-bad link
// does not repeat no-internet).
func TestDeriveRoamKeepsNoInternetState(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	roamed := *homeUp
	roamed.Path = "/ac/u-home-2"
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnLimited)), want: ""},
		{at: 12 * time.Second, want: "no-internet"},
		{at: 13 * time.Second, view: ptr(vw(nil, core.ConnNone)), want: ""},
		{at: 15 * time.Second, view: ptr(vw(&roamed, core.ConnLimited)), want: ""}, // roam: silent
		{at: 30 * time.Second, want: ""},                                           // no second no-internet
		{at: 31 * time.Second, view: ptr(vw(&roamed, core.ConnFull)), want: "internet-restored"},
	})
	// A pending (not yet fired) no-internet also survives a roam and keeps its clock.
	d = newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnPortal)), want: ""},
		{at: 2 * time.Second, view: ptr(vw(nil, core.ConnNone)), want: ""},
		{at: 4 * time.Second, view: ptr(vw(&roamed, core.ConnPortal)), want: ""},
		{at: 12 * time.Second, want: "no-internet"},
	})
	// A real disconnect still clears it: reconnecting elsewhere never says restored.
	d = newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnNone)), want: ""},
		{at: 12 * time.Second, want: "no-internet"},
		{at: 13 * time.Second, view: ptr(vw(nil, core.ConnNone)), want: ""},
		{at: 19 * time.Second, want: "disconnected"},
		{at: 20 * time.Second, view: ptr(vw(cafeUp, core.ConnFull)), want: "connected"},
	})
}

func TestDeriveSwitchNetworkWithinDebounce(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(nil, core.ConnNone)), want: ""},
		{at: 3 * time.Second, view: ptr(vw(cafeUp, core.ConnFull)), want: "connected"},
		{at: 30 * time.Second, want: ""}, // the pending disconnect was dropped
	})
	if k, gw := d.monitorNetwork(); k != "wifi:Cafe" || gw != "10.1.0.1" {
		t.Errorf("monitor network = %s %s", k, gw)
	}
}

func TestDeriveDirectSwitch(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(cafeUp, core.ConnFull)), want: "connected", check: func(t *testing.T, evs []core.Event) {
			if evs[0].Title != "Connected to Cafe" {
				t.Errorf("title = %s", evs[0].Title)
			}
		}},
	})
}

func TestDeriveCaptivePortalThenRestore(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnPortal)), want: ""},
		{at: 5 * time.Second, want: ""}, // still inside the grace
		{at: 12 * time.Second, want: "no-internet", check: func(t *testing.T, evs []core.Event) {
			e := evs[0]
			if e.Title != "No internet on HomeNet" || !strings.Contains(e.Body, "login page") || e.Data["connectivity"] != "portal" {
				t.Errorf("no-internet = %+v", e)
			}
		}},
		{at: 13 * time.Second, view: ptr(vw(homeUp, core.ConnPortal)), want: ""}, // no repeat
		{at: 14 * time.Second, view: ptr(vw(homeUp, core.ConnLimited)), want: ""},
		{at: 15 * time.Second, view: ptr(vw(homeUp, core.ConnFull)), want: "internet-restored", check: func(t *testing.T, evs []core.Event) {
			if evs[0].Body != "HomeNet is back online" || evs[0].NetworkKey != "wifi:HomeNet" {
				t.Errorf("restored = %+v", evs[0])
			}
		}},
		{at: 16 * time.Second, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
	})
}

func TestDeriveLimitedBody(t *testing.T) {
	d := newDeriver(0, 0)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnLimited)), want: "no-internet", check: func(t *testing.T, evs []core.Event) {
			if !strings.Contains(evs[0].Body, "internet is unreachable") {
				t.Errorf("body = %s", evs[0].Body)
			}
		}},
	})
}

func TestDeriveNoInternetClearedByDisconnect(t *testing.T) {
	d := newDeriver(0, 0)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnNone)), want: "no-internet"},
		{at: 2 * time.Second, view: ptr(vw(nil, core.ConnNone)), want: "disconnected"},
		// reconnecting to a healthy network must not say "restored"
		{at: 3 * time.Second, view: ptr(vw(cafeUp, core.ConnFull)), want: "connected"},
	})
}

func TestDeriveStartupOnPortal(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnPortal)), want: ""},
		{at: 11 * time.Second, want: "no-internet"},
	})
}

func TestDeriveUnknownConnectivityIsNeutral(t *testing.T) {
	d := newDeriver(0, 0)
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull)), want: ""},
		{at: time.Second, view: ptr(vw(homeUp, core.ConnUnknown)), want: ""},
		{at: 2 * time.Second, view: ptr(vw(homeUp, core.ConnNone)), want: "no-internet"},
		{at: 3 * time.Second, view: ptr(vw(homeUp, core.ConnUnknown)), want: ""},
		{at: 4 * time.Second, view: ptr(vw(homeUp, core.ConnFull)), want: "internet-restored"},
	})
}

func TestDeriveVPNUpDown(t *testing.T) {
	d := newDeriver(0, 0)
	ts := core.VPN{ID: "tailscale", Name: "Tailscale", Kind: "Tailscale", Backend: core.BackendTailscale, State: core.VPNDisconnected}
	wg := core.VPN{ID: "wg-uuid", Name: "wg-home", Kind: "WireGuard", Backend: core.BackendWireGuard, State: core.VPNDisconnected}
	tsUp, wgUp := ts, wg
	tsUp.State, wgUp.State = core.VPNConnected, core.VPNConnected
	tsConnecting := ts
	tsConnecting.State = core.VPNConnecting
	runSteps(t, d, []step{
		{at: 0, view: ptr(vw(homeUp, core.ConnFull, ts, wg)), want: ""},
		{at: 1 * time.Second, view: ptr(vw(homeUp, core.ConnFull, tsConnecting, wg)), want: ""},
		{at: 2 * time.Second, view: ptr(vw(homeUp, core.ConnFull, tsUp, wg)), want: "vpn-up", check: func(t *testing.T, evs []core.Event) {
			e := evs[0]
			if e.Title != "Tailscale connected" || e.Data["vpn"] != "tailscale" || e.Data["backend"] != "tailscale" || e.NetworkKey != "wifi:HomeNet" {
				t.Errorf("vpn-up = %+v", e)
			}
		}},
		{at: 3 * time.Second, view: ptr(vw(homeUp, core.ConnFull, tsUp, wgUp)), want: "vpn-up"},
		{at: 4 * time.Second, view: ptr(vw(homeUp, core.ConnFull, tsUp, wgUp)), want: ""},
		{at: 5 * time.Second, view: ptr(vw(homeUp, core.ConnFull, ts, wgUp)), want: "vpn-down", check: func(t *testing.T, evs []core.Event) {
			if evs[0].Title != "Tailscale disconnected" {
				t.Errorf("vpn-down = %+v", evs[0])
			}
		}},
		// a VPN vanishing from the list entirely counts as down
		{at: 6 * time.Second, view: ptr(vw(homeUp, core.ConnFull, ts)), want: "vpn-down"},
	})
}

func TestDeriveNextDeadline(t *testing.T) {
	d := newDeriver(5*time.Second, 10*time.Second)
	d.apply(vw(homeUp, core.ConnFull), t0)
	if _, ok := d.nextDeadline(); ok {
		t.Fatal("idle deriver has no deadline")
	}
	d.apply(vw(homeUp, core.ConnPortal), t0.Add(time.Second))
	if dl, ok := d.nextDeadline(); !ok || !dl.Equal(t0.Add(11*time.Second)) {
		t.Errorf("grace deadline = %v %v", dl, ok)
	}
	d.apply(vw(nil, core.ConnNone), t0.Add(2*time.Second))
	if dl, ok := d.nextDeadline(); !ok || !dl.Equal(t0.Add(7*time.Second)) {
		t.Errorf("debounce deadline = %v %v", dl, ok)
	}
}

func TestDeriveIPv6OnlyBody(t *testing.T) {
	d := newDeriver(0, 0)
	p := primary("u-home", "HomeNet", "", "", core.ActiveActivated)
	p.IPv4 = nil
	p.IPv6 = []string{"fd00::5/64"}
	d.apply(vw(nil, core.ConnNone), t0)
	evs := d.apply(vw(p, core.ConnFull), t0.Add(time.Second))
	if len(evs) != 1 || evs[0].Body != "fd00::5 via wlan0" {
		t.Errorf("events = %+v", evs)
	}
	p.IPv6 = nil
	d2 := newDeriver(0, 0)
	d2.apply(vw(nil, core.ConnNone), t0)
	evs = d2.apply(vw(p, core.ConnFull), t0.Add(time.Second))
	if len(evs) != 1 || evs[0].Body != "via wlan0" {
		t.Errorf("events = %+v", evs)
	}
}

func ptr[T any](v T) *T { return &v }
