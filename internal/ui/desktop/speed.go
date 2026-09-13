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

type speedView struct {
	a   *App
	gen atomic.Int64

	running   bool
	runBtn    *widget.Button
	quickBtn  *widget.Button
	cancelBtn *widget.Button
	cancel    context.CancelFunc
	phase     *widget.Label
	bar       *widget.ProgressBar
	live      *widget.Label
	result    *kv
	resultBox *fyne.Container
	loadErr   *errorLabel
	actErr    *errorLabel
	table     *widget.Table
	tableBox  *fyne.Container
	rows      [][]string
	empty     *widget.Label
	provider  string
}

var speedColumns = []string{"When", "Network", "Download", "Upload", "Latency", "Provider"}

func newSpeedView(a *App) *speedView { return &speedView{a: a} }

func (v *speedView) build() fyne.CanvasObject {
	v.runBtn = widget.NewButtonWithIcon("Run test", theme.MediaPlayIcon(), func() { v.run(false) })
	v.runBtn.Importance = widget.HighImportance
	v.quickBtn = widget.NewButton("Quick test", func() { v.run(true) })
	v.cancelBtn = widget.NewButton("Cancel", func() {
		if v.cancel != nil {
			v.cancel()
		}
	})
	v.cancelBtn.Hide()
	v.phase = caption("Runs only when you ask; nothing measures in the background.")
	v.bar = widget.NewProgressBar()
	v.live = bigNumber("")
	v.result = newKV("Download", "Upload", "Latency", "Jitter", "Data moved", "Server")
	v.resultBox = container.NewVBox(v.result.box)
	v.resultBox.Hide()
	v.loadErr = newErrorLabel()
	v.actErr = newErrorLabel()
	v.empty = caption("No tests yet")

	v.table = widget.NewTable(
		func() (int, int) { return len(v.rows), len(speedColumns) },
		func() fyne.CanvasObject { return mono("") },
		func(id widget.TableCellID, o fyne.CanvasObject) {
			if id.Row < len(v.rows) && id.Col < len(v.rows[id.Row]) {
				o.(*widget.Label).SetText(v.rows[id.Row][id.Col])
			}
		},
	)
	v.table.ShowHeaderRow = true
	v.table.CreateHeader = func() fyne.CanvasObject { return caption("") }
	v.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		if id.Col >= 0 && id.Col < len(speedColumns) {
			o.(*widget.Label).SetText(speedColumns[id.Col])
		}
	}
	for i, w := range []float32{130, 160, 120, 120, 90, 100} {
		v.table.SetColumnWidth(i, w)
	}

	v.tableBox = fixedSize(v.table, 0, 2*tableRowHeight)
	controls := container.NewVBox(
		container.NewHBox(v.runBtn, v.quickBtn, v.cancelBtn),
		inset(v.phase, 8, 0, 0, 0),
		v.bar,
		v.live,
		v.actErr,
	)
	return page(container.NewVBox(
		v.loadErr,
		section("Speed test", controls),
		section("Result", v.resultBox),
		section("History", container.NewVBox(v.empty, v.tableBox)),
	))
}

func (v *speedView) refresh() {
	gen := v.gen.Add(1)
	var hist []core.SpeedResult
	var provider string
	var loadErr error
	v.a.apply(func(ctx context.Context) error {
		if cfg, err := v.a.c.Config(ctx); err == nil {
			provider = cfg.Speed.Provider
		}
		hist, loadErr = v.a.c.SpeedHistory(ctx, "", 20)
		return loadErr
	}, func() {
		if gen != v.gen.Load() {
			return
		}
		v.loadErr.set(loadErr)
		if loadErr != nil {
			return
		}
		v.provider = provider
		v.rows = v.rows[:0]
		for i := len(hist) - 1; i >= 0; i-- {
			r := hist[i]
			v.rows = append(v.rows, []string{r.Time.Format("Jan 2 15:04"), r.NetworkKey, fmtMbps(r.DownloadMbps), fmtMbps(r.UploadMbps), fmtMs(r.LatencyMs), r.Provider})
		}
		if len(v.rows) == 0 {
			v.empty.Show()
			v.tableBox.Hide()
		} else {
			v.empty.Hide()
			v.tableBox.Show()
		}
		sizeTable(v.tableBox, len(v.rows))
		v.table.Refresh()
	})
}

func (v *speedView) run(quick bool) {
	if v.running {
		return
	}
	v.running = true
	v.actErr.set(nil)
	v.runBtn.Disable()
	v.quickBtn.Disable()
	v.cancelBtn.Show()
	v.bar.SetValue(0)
	v.live.SetText("")
	v.phase.SetText("Starting")
	ctx, cancel := context.WithCancel(v.a.ctx)
	v.cancel = cancel
	opts := core.SpeedOptions{Quick: quick, Provider: v.provider}
	v.a.bg(func(context.Context) {
		defer cancel()
		res, err := v.a.c.Speed(ctx, opts, func(p core.SpeedProgress) {
			v.a.onUI(func() { v.progress(p) })
		})
		v.a.onUI(func() { v.finish(res, err) })
	})
}

// progress is the live phase/bar/Mbit update, on the UI thread.
func (v *speedView) progress(p core.SpeedProgress) {
	v.bar.SetValue(p.Percent / 100)
	switch p.Phase {
	case "latency":
		v.phase.SetText("Measuring latency")
	case "download":
		v.phase.SetText("Downloading")
	case "upload":
		v.phase.SetText("Uploading")
	case "done":
		v.phase.SetText("Done")
	default:
		v.phase.SetText(p.Phase)
	}
	if p.Mbps > 0 {
		v.live.SetText(fmtMbps(p.Mbps))
	}
}

func (v *speedView) finish(res core.SpeedResult, err error) {
	v.running = false
	v.runBtn.Enable()
	v.quickBtn.Enable()
	v.cancelBtn.Hide()
	v.cancel = nil
	if err != nil {
		v.phase.SetText("Failed")
		v.actErr.set(err)
		v.live.SetText("")
		return
	}
	v.bar.SetValue(1)
	v.phase.SetText("Done")
	v.live.SetText(fmt.Sprintf("%s down  %s up", fmtMbps(res.DownloadMbps), fmtMbps(res.UploadMbps)))
	v.result.setAll(fmtMbps(res.DownloadMbps), fmtMbps(res.UploadMbps), fmtMs(res.LatencyMs), fmtMs(res.JitterMs), fmtBytes(res.BytesMoved), res.Server)
	v.resultBox.Show()
	v.resultBox.Refresh()
	v.refresh()
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
