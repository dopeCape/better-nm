package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Default timing of the event deriver, per the notification policy (issue #25).
const (
	// DefaultDebounce is the window inside which a disconnect followed by a
	// reconnect to the same network (Wi-Fi roaming) emits nothing.
	DefaultDebounce = 5 * time.Second
	// DefaultConnectivityGrace is how long NM's connectivity must stay bad
	// before no-internet fires; NM reports limited/unknown for a moment after
	// every connect while its check runs.
	DefaultConnectivityGrace = 10 * time.Second
)

// view is the slice of the snapshot the deriver reasons about.
type view struct {
	Status   core.Status
	Profiles []core.Profile
	VPNs     []core.VPN
}

// deriver turns successive views of the world into Events. It is a pure state
// machine: apply feeds it a new view at a given time, flush fires timers, and
// nextDeadline says when flush next has work. No goroutines, no clocks.
type deriver struct {
	debounce time.Duration
	grace    time.Duration

	initialized bool
	primary     *core.ActiveConnection // last activated primary, nil when none
	primaryKey  string
	primaryName string
	noInternet  bool // a no-internet event is outstanding

	pendingDisc       *pendingDisconnect
	pendingNoInternet *pendingNoInternet
	vpnUp             map[string]core.VPN // id -> VPN, as of the last view, for those connected

	// monitorKey/monitorGateway is the network the probe engine should be on:
	// it follows connected/disconnected *events*, so a roam never resets it.
	monitorKey     string
	monitorGateway string
}

type pendingDisconnect struct {
	key, name, uuid string
	at              time.Time
}

type pendingNoInternet struct {
	conn  core.Connectivity
	since time.Time
}

func newDeriver(debounce, grace time.Duration) *deriver {
	if debounce < 0 {
		debounce = 0
	}
	if grace < 0 {
		grace = 0
	}
	return &deriver{debounce: debounce, grace: grace, vpnUp: map[string]core.VPN{}}
}

// connectedPrimary returns the primary when it is really up.
func connectedPrimary(s core.Status) *core.ActiveConnection {
	p := s.Primary
	if p == nil {
		return nil
	}
	switch p.State {
	case core.ActiveActivated, "":
		return p
	}
	return nil
}

func isBad(c core.Connectivity) bool {
	switch c {
	case core.ConnNone, core.ConnLimited, core.ConnPortal:
		return true
	}
	return false
}

// apply ingests a view and returns the events it implies, including any timers
// that expired at now.
func (d *deriver) apply(v view, now time.Time) []core.Event {
	var out []core.Event
	cur := connectedPrimary(v.Status)
	key := core.NetworkKey(cur, v.Profiles)

	if !d.initialized {
		d.initialized = true
		d.setPrimary(cur, key)
		d.monitorKey, d.monitorGateway = key, gatewayOf(cur)
		for _, vpn := range v.VPNs {
			if vpn.State == core.VPNConnected {
				d.vpnUp[vpn.ID] = vpn
			}
		}
		if cur != nil && isBad(v.Status.Connectivity) {
			d.pendingNoInternet = &pendingNoInternet{conn: v.Status.Connectivity, since: now}
		}
		return d.flush(now)
	}

	switch {
	case d.primary == nil && cur != nil:
		roam := false
		if pd := d.pendingDisc; pd != nil {
			d.pendingDisc = nil
			roam = pd.key == key
		}
		d.setPrimary(cur, key)
		if !roam {
			d.resetConnectivity()
			d.monitorKey, d.monitorGateway = key, gatewayOf(cur)
			out = append(out, connectedEvent(cur, key, now))
		}
	case d.primary != nil && cur == nil:
		// The connectivity state is kept until the disconnect is confirmed
		// (flush) or a different network connects: a roam must leave an
		// outstanding no-internet in place so the later "full" says restored.
		d.pendingDisc = &pendingDisconnect{key: d.primaryKey, name: d.primaryName, uuid: d.primary.ProfileUUID, at: now}
		d.setPrimary(nil, "")
	case d.primary != nil && cur != nil && cur.ProfileUUID != d.primary.ProfileUUID:
		d.pendingDisc = nil
		d.setPrimary(cur, key)
		d.resetConnectivity()
		d.monitorKey, d.monitorGateway = key, gatewayOf(cur)
		out = append(out, connectedEvent(cur, key, now))
	case d.primary != nil && cur != nil:
		d.setPrimary(cur, key) // same profile: refresh addresses
		if gw := gatewayOf(cur); gw != "" && d.monitorKey == key {
			d.monitorGateway = gw
		}
	}

	if d.primary != nil {
		conn := v.Status.Connectivity
		switch {
		case isBad(conn) && !d.noInternet:
			if d.pendingNoInternet == nil {
				d.pendingNoInternet = &pendingNoInternet{conn: conn, since: now}
			} else {
				d.pendingNoInternet.conn = conn
			}
		case conn == core.ConnFull:
			d.pendingNoInternet = nil
			if d.noInternet {
				d.noInternet = false
				out = append(out, restoredEvent(d.primaryName, d.primaryKey, now))
			}
		}
	}

	seen := map[string]core.VPN{}
	for _, vpn := range v.VPNs {
		if vpn.State != core.VPNConnected {
			continue
		}
		seen[vpn.ID] = vpn
		if _, was := d.vpnUp[vpn.ID]; !was {
			out = append(out, vpnEvent(core.EventVPNUp, vpn, d.primaryKey, now))
		}
	}
	for id, vpn := range d.vpnUp {
		if _, still := seen[id]; !still {
			out = append(out, vpnEvent(core.EventVPNDown, vpn, d.primaryKey, now))
		}
	}
	d.vpnUp = seen

	return append(out, d.flush(now)...)
}

// flush fires timers that expired by now.
func (d *deriver) flush(now time.Time) []core.Event {
	var out []core.Event
	if pd := d.pendingDisc; pd != nil && !now.Before(pd.at.Add(d.debounce)) {
		d.pendingDisc = nil
		d.monitorKey, d.monitorGateway = "", ""
		d.resetConnectivity()
		out = append(out, disconnectedEvent(pd, now))
	}
	if pn := d.pendingNoInternet; pn != nil && d.primary != nil && !now.Before(pn.since.Add(d.grace)) {
		d.pendingNoInternet = nil
		d.noInternet = true
		out = append(out, noInternetEvent(d.primaryName, d.primaryKey, pn.conn, now))
	}
	return out
}

// nextDeadline reports when flush next has something to do.
func (d *deriver) nextDeadline() (time.Time, bool) {
	var t time.Time
	if pd := d.pendingDisc; pd != nil {
		t = pd.at.Add(d.debounce)
	}
	if pn := d.pendingNoInternet; pn != nil && d.primary != nil {
		if u := pn.since.Add(d.grace); t.IsZero() || u.Before(t) {
			t = u
		}
	}
	return t, !t.IsZero()
}

// monitorNetwork is the (key, gateway) the probe engine should be on.
func (d *deriver) monitorNetwork() (string, string) {
	return d.monitorKey, d.monitorGateway
}

func (d *deriver) setPrimary(p *core.ActiveConnection, key string) {
	if p == nil {
		d.primary, d.primaryKey, d.primaryName = nil, "", ""
		return
	}
	cp := *p
	d.primary, d.primaryKey, d.primaryName = &cp, key, p.ProfileName
}

func (d *deriver) resetConnectivity() {
	d.noInternet = false
	d.pendingNoInternet = nil
}

func gatewayOf(p *core.ActiveConnection) string {
	if p == nil {
		return ""
	}
	return p.Gateway4
}

func firstIP(p *core.ActiveConnection) string {
	if len(p.IPv4) > 0 {
		return strings.SplitN(p.IPv4[0], "/", 2)[0]
	}
	if len(p.IPv6) > 0 {
		return strings.SplitN(p.IPv6[0], "/", 2)[0]
	}
	return ""
}

func connectedEvent(p *core.ActiveConnection, key string, now time.Time) core.Event {
	dev := ""
	if len(p.Devices) > 0 {
		dev = p.Devices[0]
	}
	body := ""
	switch ip := firstIP(p); {
	case ip != "" && dev != "":
		body = fmt.Sprintf("%s via %s", ip, dev)
	case ip != "":
		body = ip
	case dev != "":
		body = "via " + dev
	}
	return core.Event{
		Time: now, Type: core.EventConnected, NetworkKey: key,
		Title: "Connected to " + p.ProfileName, Body: body, Urgency: "low",
		Data: map[string]string{"uuid": p.ProfileUUID, "device": dev, "ip": firstIP(p), "gateway": p.Gateway4},
	}
}

func disconnectedEvent(pd *pendingDisconnect, now time.Time) core.Event {
	return core.Event{
		Time: now, Type: core.EventDisconnected, NetworkKey: pd.key,
		Title: "Disconnected from " + pd.name, Body: "No network connection", Urgency: "normal",
		Data: map[string]string{"uuid": pd.uuid},
	}
}

func noInternetEvent(name, key string, conn core.Connectivity, now time.Time) core.Event {
	body := fmt.Sprintf("Connected to %s but the internet is unreachable", name)
	if conn == core.ConnPortal {
		body = fmt.Sprintf("%s needs a login page: open a browser to sign in", name)
	}
	return core.Event{
		Time: now, Type: core.EventNoInternet, NetworkKey: key,
		Title: "No internet on " + name, Body: body, Urgency: "normal",
		Data: map[string]string{"connectivity": string(conn)},
	}
}

func restoredEvent(name, key string, now time.Time) core.Event {
	return core.Event{
		Time: now, Type: core.EventInternetRestored, NetworkKey: key,
		Title: "Internet restored", Body: name + " is back online", Urgency: "low",
	}
}

func vpnEvent(t core.EventType, vpn core.VPN, key string, now time.Time) core.Event {
	title := vpn.Name + " connected"
	body := "VPN is up"
	if t == core.EventVPNDown {
		title = vpn.Name + " disconnected"
		body = "VPN is down"
	}
	if vpn.Kind != "" {
		body += " (" + vpn.Kind + ")"
	}
	return core.Event{
		Time: now, Type: t, NetworkKey: key, Title: title, Body: body, Urgency: "low",
		Data: map[string]string{"vpn": vpn.ID, "backend": string(vpn.Backend)},
	}
}
