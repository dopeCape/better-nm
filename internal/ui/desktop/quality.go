package desktop

import (
	"context"
	"fmt"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/core"
)

type qualityData struct {
	monitor core.MonitorStatus
	samples map[string][]core.Sample // by anchor
	err     error
}

type qualityView struct {
	a   *App
	gen atomic.Int64

	loadErr  *errorLabel
	actErr   *errorLabel
	stateDot *dot
	state    *widget.Label
	since    *widget.Label
	network  *widget.Label
	pause    *widget.Button
	reset    *widget.Button
	dns      *widget.Label
	table    *widget.Table
	tableBox *fyne.Container
	rows     [][]string
	charts   *fyne.Container
	sparks   map[string]*sparkline
	paused   bool
}

var qualityColumns = []string{"Anchor", "State", "RTT now", "Baseline", "Loss", "Samples"}

func newQualityView(a *App) *qualityView { return &qualityView{a: a, sparks: map[string]*sparkline{}} }

func (v *qualityView) build() fyne.CanvasObject {
	v.loadErr = newErrorLabel()
	v.actErr = newErrorLabel()
	v.stateDot = newDot(10)
	v.state = widget.NewLabel("unknown")
	v.state.TextStyle = fyne.TextStyle{Bold: true}
	v.state.SizeName = theme.SizeNameHeadingText
	v.since = caption("")
	v.network = mono("")
	v.pause = widget.NewButtonWithIcon("Pause", theme.MediaPauseIcon(), v.togglePause)
	v.reset = widget.NewButton("Reset baseline", v.resetBaseline)
	v.dns = mono("")

	bannerRow := container.NewBorder(nil, nil,
		container.NewHBox(inset(v.stateDot, 0, 8, 0, 0), v.state, inset(v.since, 8, 0, 0, 12)),
		container.NewHBox(v.pause, v.reset),
	)
	bannerBox := container.NewVBox(bannerRow, inset(container.NewHBox(caption("Network"), v.network, inset(caption("DNS"), 0, 0, 0, 16), v.dns), 0, 0, 0, 0), v.actErr)

	v.table = widget.NewTable(
		func() (int, int) { return len(v.rows), len(qualityColumns) },
		func() fyne.CanvasObject { return mono("") },
		func(id widget.TableCellID, o fyne.CanvasObject) {
			l := o.(*widget.Label)
			if id.Row < len(v.rows) && id.Col < len(v.rows[id.Row]) {
				l.SetText(v.rows[id.Row][id.Col])
			}
		},
	)
	v.table.ShowHeaderRow = true
	v.table.CreateHeader = func() fyne.CanvasObject { return caption("") }
	v.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		if id.Col >= 0 && id.Col < len(qualityColumns) {
			o.(*widget.Label).SetText(qualityColumns[id.Col])
		}
	}
	for i, w := range []float32{140, 90, 90, 90, 70, 80} {
		v.table.SetColumnWidth(i, w)
	}
	v.charts = container.NewVBox()
	v.tableBox = fixedSize(v.table, 0, 2*tableRowHeight)

	return page(container.NewVBox(
		v.loadErr,
		section("Baseline", bannerBox),
		section("Anchors", v.tableBox),
		section("Round-trip time", v.charts),
	))
}

func (v *qualityView) refresh() {
	gen := v.gen.Add(1)
	var d qualityData
	v.a.apply(func(ctx context.Context) error {
		var err error
		if d.monitor, err = v.a.c.Monitor(ctx); err != nil {
			d.err = err
			return err
		}
		d.samples = map[string][]core.Sample{}
		for _, b := range d.monitor.Anchors {
			d.samples[b.Anchor], _ = v.a.c.Samples(ctx, d.monitor.NetworkKey, b.Anchor, 120)
		}
		return nil
	}, func() {
		if gen != v.gen.Load() {
			return
		}
		v.render(d)
	})
}

func (v *qualityView) render(d qualityData) {
	v.loadErr.set(d.err)
	if d.err != nil {
		return
	}
	m := d.monitor
	v.paused = m.Paused
	hs := headerState{monitor: &m}
	verdict := hs.verdict()
	v.state.SetText(verdict)
	v.stateDot.setColor(semanticColor(verdict))
	if m.Paused {
		v.pause.SetText("Resume")
		v.pause.SetIcon(theme.MediaPlayIcon())
	} else {
		v.pause.SetText("Pause")
		v.pause.SetIcon(theme.MediaPauseIcon())
	}
	v.network.SetText(m.NetworkKey)
	var since string
	var dnsMs float64 = -1
	v.rows = v.rows[:0]
	for _, b := range m.Anchors {
		if !b.Since.IsZero() && since == "" {
			since = "since " + fmtAgo(b.Since)
		}
		if b.CurrentDNS > 0 {
			dnsMs = b.CurrentDNS
		}
		v.rows = append(v.rows, []string{b.Anchor, string(b.State), fmtMs(b.CurrentRTT), fmtMs(b.BaselineRTT), fmtPct(b.CurrentLoss), fmt.Sprintf("%d", b.SampleCount)})
	}
	if len(m.Anchors) == 0 {
		v.rows = append(v.rows, []string{"no anchors", "", "", "", "", ""})
	}
	if !m.LastSample.IsZero() && since == "" {
		since = "last sample " + fmtAgo(m.LastSample)
	}
	v.since.SetText(since)
	if dnsMs >= 0 {
		v.dns.SetText(fmtMs(dnsMs))
	} else {
		v.dns.SetText("-")
	}
	sizeTable(v.tableBox, len(v.rows))
	v.table.Refresh()

	v.charts.Objects = nil
	for _, b := range m.Anchors {
		sp, ok := v.sparks[b.Anchor]
		if !ok {
			sp = newSparkline(72)
			v.sparks[b.Anchor] = sp
		}
		vals := make([]float64, 0, len(d.samples[b.Anchor]))
		for _, s := range d.samples[b.Anchor] {
			vals = append(vals, s.RTTms)
		}
		sp.set(vals, b.BaselineRTT)
		title := container.NewHBox(bold(b.Anchor), spacer(), caption(fmt.Sprintf("%d samples, baseline %s", len(vals), fmtMs(b.BaselineRTT))))
		v.charts.Add(inset(container.NewVBox(title, sp), 0, 0, 12, 0))
	}
	if len(m.Anchors) == 0 {
		v.charts.Add(caption("The monitor has no anchors on this network yet."))
	}
	v.charts.Refresh()
}

func (v *qualityView) togglePause() {
	v.actErr.set(nil)
	paused := v.paused
	v.a.bg(func(ctx context.Context) {
		var err error
		if paused {
			err = v.a.c.ResumeMonitor(ctx)
		} else {
			err = v.a.c.PauseMonitor(ctx)
		}
		if err != nil {
			v.a.onUI(func() { v.actErr.set(err) })
			return
		}
		v.refresh()
	})
}

func (v *qualityView) resetBaseline() {
	v.actErr.set(nil)
	v.a.bg(func(ctx context.Context) {
		if err := v.a.c.ResetBaseline(ctx, ""); err != nil {
			v.a.onUI(func() { v.actErr.set(err) })
			return
		}
		v.refresh()
	})
}
