package desktop

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/core"
)

const maxRecentEvents = 5

// overviewData is one fetched snapshot for the overview.
type overviewData struct {
	status  api.StatusResponse
	devices []core.Device
	wifi    []core.WifiNetwork
	vpns    []core.VPN
	monitor core.MonitorStatus
	samples []core.Sample
	events  []core.Event
	err     error
}

type overviewView struct {
	a   *App
	gen atomic.Int64

	conn     *kv
	vpnRows  *fyne.Container
	vpnEmpty *widget.Label
	verdict  *widget.Label
	rtt      *widget.Label
	spark    *sparkline
	events   *fyne.Container
	loadErr  *errorLabel
	actErr   *errorLabel
	seeded   bool
}

func newOverviewView(a *App) *overviewView { return &overviewView{a: a} }

func (v *overviewView) build() fyne.CanvasObject {
	v.conn = newKV("Name", "IP", "Gateway", "DNS", "Signal")
	v.vpnRows = container.NewVBox()
	v.vpnEmpty = caption("No VPNs configured")
	v.verdict = bold("Unknown")
	v.rtt = mono("")
	v.spark = newSparkline(56)
	v.events = container.NewVBox(caption("No events yet"))
	v.loadErr = newErrorLabel()
	v.actErr = newErrorLabel()

	left := container.NewVBox(
		section("Connection", v.conn.box),
		section("VPN", container.NewVBox(v.vpnEmpty, v.vpnRows, v.actErr)),
	)
	quality := container.NewVBox(
		container.NewHBox(v.verdict, spacer(), v.rtt),
		inset(v.spark, 8, 0, 0, 0),
	)
	right := container.NewVBox(
		section("Quality", quality),
		section("Recent events", v.events),
	)
	grid := container.NewGridWithColumns(2, inset(left, 0, 16, 0, 0), inset(right, 0, 0, 0, 16))
	return page(container.NewVBox(v.loadErr, grid))
}

func (v *overviewView) refresh() {
	gen := v.gen.Add(1)
	var d overviewData
	v.a.apply(func(ctx context.Context) error {
		var err error
		if d.status, err = v.a.c.Status(ctx); err != nil {
			d.err = err
			return err
		}
		d.devices, _ = v.a.c.Devices(ctx)
		d.wifi, _ = v.a.c.Wifi(ctx, "")
		d.vpns, _ = v.a.c.VPNs(ctx)
		d.monitor, _ = v.a.c.Monitor(ctx)
		d.samples, _ = v.a.c.Samples(ctx, "", "gateway", 60)
		if !v.seeded {
			d.events, _ = v.a.c.EventHistory(ctx, maxRecentEvents)
		}
		return nil
	}, func() {
		if gen != v.gen.Load() {
			return
		}
		v.render(d)
	})
}

func (v *overviewView) render(d overviewData) {
	v.loadErr.set(d.err)
	if d.err != nil {
		return
	}
	// connection
	p := d.status.Primary
	if p == nil {
		v.conn.setAll("Not connected", "", "", "", "")
	} else {
		signal := ""
		for _, w := range d.wifi {
			if w.Active {
				signal = fmt.Sprintf("%d%%  %s  ch %d", w.Strength, bandName(w.Band), w.Channel)
			}
		}
		if signal == "" {
			for _, dev := range d.devices {
				if dev.ActiveUUID == p.ProfileUUID && dev.Speed > 0 {
					signal = fmt.Sprintf("%d Mbit/s link", dev.Speed)
				}
			}
		}
		v.conn.setAll(p.ProfileName, firstOr(p.IPv4, firstOr(p.IPv6, "")), p.Gateway4, joinOr(p.DNS, ""), signal)
	}
	// vpns
	v.vpnRows.Objects = nil
	for _, vpn := range d.vpns {
		v.vpnRows.Add(v.vpnRow(vpn))
	}
	if len(d.vpns) == 0 {
		v.vpnEmpty.Show()
	} else {
		v.vpnEmpty.Hide()
	}
	v.vpnRows.Refresh()
	// quality
	hs := headerState{monitor: &d.monitor}
	v.verdict.SetText(verdictLabel(hs.verdict())) // the section is already titled Quality
	var gw *core.Baseline
	for i := range d.monitor.Anchors {
		if d.monitor.Anchors[i].Anchor == "gateway" {
			gw = &d.monitor.Anchors[i]
		}
	}
	if gw != nil && gw.CurrentRTT > 0 {
		v.rtt.SetText(fmt.Sprintf("gateway %s  baseline %s", fmtMs(gw.CurrentRTT), fmtMs(gw.BaselineRTT)))
	} else {
		v.rtt.SetText("")
	}
	vals := make([]float64, 0, len(d.samples))
	for _, s := range d.samples {
		vals = append(vals, s.RTTms)
	}
	var base float64
	if gw != nil {
		base = gw.BaselineRTT
	}
	v.spark.set(vals, base)
	// events (history seeds once; live events arrive through setEvents)
	if !v.seeded && d.events != nil {
		v.seeded = true
		v.a.eventsMu.Lock()
		if len(v.a.events) == 0 {
			v.a.events = append(v.a.events, d.events...)
			if len(v.a.events) > maxRecentEvents {
				v.a.events = v.a.events[len(v.a.events)-maxRecentEvents:]
			}
		}
		recent := append([]core.Event(nil), v.a.events...)
		v.a.eventsMu.Unlock()
		v.setEvents(recent)
	}
}

func (v *overviewView) vpnRow(vpn core.VPN) fyne.CanvasObject {
	id := vpn.ID
	on := vpn.State == core.VPNConnected || vpn.State == core.VPNConnecting
	check := widget.NewCheck("", nil)
	check.SetChecked(on)
	if !vpn.Writable || vpn.State == core.VPNUnavailable {
		check.Disable()
	}
	check.OnChanged = func(want bool) {
		v.actErr.set(nil)
		v.a.bg(func(ctx context.Context) {
			var err error
			if want {
				err = v.a.c.ConnectVPN(ctx, id)
			} else {
				err = v.a.c.DisconnectVPN(ctx, id)
			}
			if err != nil {
				v.a.onUI(func() { v.actErr.set(err); setCheckedSilently(check, !want) })
			}
		})
	}
	state := caption(vpnStateText(vpn))
	return container.NewHBox(check, bold(vpn.Name), kindBadge(vpn), spacer(), state)
}

// vpnStateText is the short state line shown next to a VPN.
func vpnStateText(vpn core.VPN) string {
	s := string(vpn.State)
	switch vpn.State {
	case core.VPNConnected:
		if vpn.Tailscale != nil && vpn.Tailscale.ExitNodeOn && vpn.Tailscale.ExitNodeName != "" {
			return "connected via " + vpn.Tailscale.ExitNodeName
		}
		return "connected"
	case core.VPNError:
		if vpn.Error != "" {
			return "error: " + vpn.Error
		}
	case core.VPNNeedsSetup, core.VPNNeedsAuth, core.VPNUnavailable:
		if vpn.Detail != "" {
			return s + ": " + vpn.Detail
		}
	}
	return s
}

func (v *overviewView) setEvents(events []core.Event) {
	v.events.Objects = nil
	if len(events) == 0 {
		v.events.Add(caption("No events yet"))
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		title := widget.NewLabel(e.Title)
		title.Truncation = fyne.TextTruncateEllipsis
		when := caption(fmtAgo(e.Time))
		v.events.Add(container.NewBorder(nil, nil, nil, when, title))
	}
	v.events.Refresh()
}

// verdictLabel renders a monitor verdict as a standalone word: "ok" reads as "OK",
// the rest are capitalised.
func verdictLabel(v string) string {
	if v == "ok" {
		return "OK"
	}
	if v == "" {
		return "Unknown"
	}
	return strings.ToUpper(v[:1]) + v[1:]
}
