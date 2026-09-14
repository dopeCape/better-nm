// Package desktop is the bnm desktop app: a Fyne window (and, when a
// StatusNotifierItem host exists, a tray icon) over the daemon's Go client.
// It owns the theme, the eight views, the live refresh loop and the tray menu;
// cmd/bnm-desktop is a thin main around New and Run.
//
// # Design read
//
// Reading this as: a desktop utility for one technical Linux user (the person
// who runs Hyprland or KDE and wants NetworkManager not to suck), with a quiet
// instrument-panel language, leaning toward a neutral monochrome palette with a
// single cobalt accent, hairline separators instead of cards, and monospace for
// every machine value (addresses, RTTs, Mbit/s).
//
// Dials: DESIGN_VARIANCE 4 (a fixed sidebar + content grid; the only asymmetry
// is the 200 px nav column), MOTION_INTENSITY 2 (no decorative motion; only
// state feedback: progress bar, connecting labels), VISUAL_DENSITY 6 (a daily
// tool: 13 px text on a 4/8/12/16/24 spacing rhythm, sections divided by
// hairlines, tables for anything with more than five rows).
//
// Palette: light ground #F3F3F1 / dark ground #151618 (never pure white or
// black), foreground #212226 / #E4E4E1, one accent (#2E6FD8 light, #79A8F0
// dark) reserved for selection, primary buttons and sparklines; the semantic
// colours ok / degraded / down (green / amber / red) are separate so the accent
// never lies about status. The dark variant is its own palette, not an
// inversion. Type is Fyne's bundled Noto Sans (no font shipped) at 13 / 11
// caption / 15 subheading / 20 heading, monospace for values. One corner
// radius (4 px) everywhere.
//
// # Threading
//
// The daemon client is never called on the Fyne thread: every fetch or action
// runs in a goroutine started by App.bg and pushes its result back through
// App.onUI (fyne.Do). Views render from a snapshot handed to them on the UI
// thread. One goroutine follows the daemon's event stream, reconnecting with
// backoff and raising a banner while the daemon is unreachable.
//
// # Testing
//
// Tests use Fyne's test driver and an in-process API server over a temp Unix
// socket backed by internal/fake, then assert widget content after waiting for
// the app to go idle. `go test -race ./internal/ui/desktop/...` needs cgo and
// the GL headers, so run it inside `nix develop`.
package desktop

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// Options configures New.
type Options struct {
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Tray forces the tray on (true) or off (false); nil autodetects a
	// StatusNotifierItem host on the session bus.
	Tray *bool
	// InstallService replaces the user-service installer (tests stub it).
	InstallService func(ctx context.Context) (string, error)
}

// Section is one entry of the sidebar.
type Section int

// The sidebar entries, in display order.
const (
	SectionOverview Section = iota
	SectionWifi
	SectionVPN
	SectionQuality
	SectionSpeed
	SectionDevices
	SectionAdvanced
	SectionSettings
	sectionCount
)

var sectionNames = [sectionCount]string{"Overview", "Wi-Fi", "VPN", "Quality", "Speed", "Devices", "Advanced", "Settings"}

// ParseSection maps a name like "wifi" to its Section (case-insensitive; the
// hyphen in "wi-fi" is optional).
func ParseSection(name string) (Section, bool) {
	name = strings.ToLower(strings.ReplaceAll(name, "-", ""))
	for i, n := range sectionNames {
		if strings.ToLower(strings.ReplaceAll(n, "-", "")) == name {
			return Section(i), true
		}
	}
	return 0, false
}

func (s Section) String() string {
	if s >= 0 && s < sectionCount {
		return sectionNames[s]
	}
	return "?"
}

// view is one content pane. build is called once on the UI thread; refresh
// fetches off-thread and re-renders.
type view interface {
	build() fyne.CanvasObject
	refresh()
}

// changeDeps says which views a Change kind invalidates (the header and the
// tray always follow status/active/vpn/monitor).
var changeDeps = map[core.ChangeKind][]Section{
	core.ChangeStatus:   {SectionOverview, SectionDevices},
	core.ChangeDevices:  {SectionOverview, SectionDevices, SectionAdvanced},
	core.ChangeWifi:     {SectionWifi},
	core.ChangeProfiles: {SectionWifi, SectionVPN, SectionDevices},
	core.ChangeActive:   {SectionOverview, SectionWifi, SectionDevices, SectionQuality},
	core.ChangeVPN:      {SectionOverview, SectionVPN},
	core.ChangeMonitor:  {SectionOverview, SectionQuality},
}

// App is the running desktop application.
type App struct {
	fy   fyne.App
	win  fyne.Window
	c    *client.Client
	log  *slog.Logger
	opts Options

	ctx    context.Context
	cancel context.CancelFunc

	header  *header
	banner  *banner
	nav     *widget.List
	content *fyne.Container
	views   [sectionCount]view
	panes   [sectionCount]fyne.CanvasObject
	current Section

	tray *tray

	// idle tracks work started by bg so tests can wait for quiescence.
	idleMu   sync.Mutex
	idleCond *sync.Cond
	inflight int
	// uiMu serialises UI mutations under the test driver (which runs fyne.Do
	// inline on the calling goroutine); on the real driver it is uncontended.
	uiMu sync.Mutex

	// events keeps the last few daemon events for the overview.
	eventsMu sync.Mutex
	events   []core.Event
	offline  bool

	// Secret prompts (UI thread only): the open dialog, the ones behind it
	// and every id shown or queued so a late event is not a duplicate.
	secret      *secretPrompt
	secretQueue []core.SecretRequest
	secretKnown map[string]bool
}

// New builds the window and views; nothing is fetched until Run (or, in
// tests, Start).
func New(fy fyne.App, c *client.Client, opts Options) *App {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	a := &App{fy: fy, c: c, log: opts.Logger, opts: opts, secretKnown: map[string]bool{}}
	a.idleCond = sync.NewCond(&a.idleMu)
	a.ctx, a.cancel = context.WithCancel(context.Background())
	fy.Settings().SetTheme(newTheme())
	if icon := appIcon(); icon != nil {
		fy.SetIcon(icon)
	}
	a.win = fy.NewWindow("bnm")
	a.win.Resize(fyne.NewSize(1120, 720))
	a.buildUI()
	return a
}

// Window is the main window.
func (a *App) Window() fyne.Window { return a.win }

// Run shows the window, starts the live refresh loop and the tray, and blocks
// until the app quits.
func (a *App) Run() {
	a.Start()
	a.win.ShowAndRun()
	a.cancel()
}

// Start starts the background work (first load, event stream, tray) without
// entering the main loop; Run calls it.
func (a *App) Start() {
	a.setupTray()
	a.win.SetCloseIntercept(func() {
		if a.tray != nil {
			a.win.Hide()
			return
		}
		a.Quit()
	})
	a.refreshAll()
	a.loadPendingSecrets()
	go a.streamLoop()
}

// Quit stops background work and exits the app.
func (a *App) Quit() {
	a.cancel()
	a.fy.Quit()
}

func (a *App) buildUI() {
	a.header = newHeader()
	a.banner = newBanner()
	a.views = [sectionCount]view{
		SectionOverview: newOverviewView(a),
		SectionWifi:     newWifiView(a),
		SectionVPN:      newVPNView(a),
		SectionQuality:  newQualityView(a),
		SectionSpeed:    newSpeedView(a),
		SectionDevices:  newDevicesView(a),
		SectionAdvanced: newAdvancedView(a),
		SectionSettings: newSettingsView(a),
	}
	for i, v := range a.views {
		a.panes[i] = v.build()
	}
	a.content = container.NewStack(a.panes[SectionOverview])
	a.nav = widget.NewList(
		func() int { return int(sectionCount) },
		func() fyne.CanvasObject { return widget.NewLabel("Overview") },
		func(id widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(Section(id).String()) },
	)
	a.nav.HideSeparators = true
	a.nav.OnSelected = func(id widget.ListItemID) { a.show(Section(id)) }
	a.nav.Select(int(SectionOverview))

	side := container.NewBorder(
		inset(brand(), 16, 12, 8, 16), nil, nil, nil,
		a.nav,
	)
	sideBox := fixedWidth(side, 200)
	top := container.NewVBox(a.header.content(), a.banner.content(), widget.NewSeparator())
	body := container.NewBorder(top, nil, container.NewHBox(sideBox, widget.NewSeparator()), nil, a.content)
	a.win.SetContent(body)
}

// show switches the content pane.
func (a *App) show(s Section) {
	if s < 0 || s >= sectionCount {
		return
	}
	a.current = s
	a.content.Objects = []fyne.CanvasObject{a.panes[s]}
	a.content.Refresh()
}

// Select selects a section on the UI thread (before Run, or from a UI callback).
func (a *App) Select(s Section) {
	a.nav.Select(int(s))
}

// --- background work and thread marshalling -------------------------------------

// bg runs fn off the UI thread with the app context; it is tracked so
// waitIdle can wait for it. Call from anywhere.
func (a *App) bg(fn func(ctx context.Context)) {
	a.idleMu.Lock()
	a.inflight++
	a.idleMu.Unlock()
	go func() {
		defer a.done()
		fn(a.ctx)
	}()
}

func (a *App) done() {
	a.idleMu.Lock()
	a.inflight--
	if a.inflight == 0 {
		a.idleCond.Broadcast()
	}
	a.idleMu.Unlock()
}

// onUI runs fn on the Fyne thread. Only call it from a goroutine you started
// (a bg func), never from UI callbacks: those are already on the UI thread and
// under the test driver a nested call would deadlock on uiMu.
func (a *App) onUI(fn func()) {
	a.idleMu.Lock()
	a.inflight++
	a.idleMu.Unlock()
	fyne.Do(func() {
		defer a.done()
		a.uiMu.Lock()
		defer a.uiMu.Unlock()
		fn()
	})
}

// waitIdle blocks until no bg/onUI work is pending, or the timeout passes.
func (a *App) waitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	a.idleMu.Lock()
	defer a.idleMu.Unlock()
	for a.inflight > 0 {
		if time.Now().After(deadline) {
			return false
		}
		t := time.AfterFunc(50*time.Millisecond, a.idleCond.Broadcast)
		a.idleCond.Wait()
		t.Stop()
	}
	return true
}

// refreshAll reloads the header and every view.
func (a *App) refreshAll() {
	a.refreshHeader()
	for _, v := range a.views {
		v.refresh()
	}
}

// apply is the shape of every view refresh: fetch off-thread, then render on
// the UI thread. Errors reach render as err so views can show them inline.
func (a *App) apply(fetch func(ctx context.Context) error, render func()) {
	a.bg(func(ctx context.Context) {
		if err := fetch(ctx); err != nil && ctx.Err() == nil {
			a.log.Debug("fetch", "err", err)
		}
		a.onUI(render)
	})
}

// --- header and live refresh ------------------------------------------------------

func (a *App) refreshHeader() {
	var st headerState
	a.apply(func(ctx context.Context) error {
		s, err := a.c.Status(ctx)
		if err != nil {
			return err
		}
		st.status = &s
		if m, err := a.c.Monitor(ctx); err == nil {
			st.monitor = &m
		}
		return nil
	}, func() {
		if st.status != nil {
			a.header.set(st)
			if a.tray != nil {
				a.tray.setStatus(st)
			}
		}
	})
}

// streamLoop follows the daemon's event stream, reconnecting with backoff.
func (a *App) streamLoop() {
	backoff := time.Second
	for a.ctx.Err() == nil {
		ch, err := a.c.Events(a.ctx)
		if err != nil {
			if a.ctx.Err() != nil {
				return
			}
			a.setOffline(true)
			a.log.Debug("event stream", "err", err, "retry_in", backoff)
			select {
			case <-a.ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		if a.offlineNow() {
			a.setOffline(false)
			a.refreshAll()
			a.loadPendingSecrets()
		}
		backoff = time.Second
		for it := range ch {
			switch {
			case it.Change != nil:
				a.onChange(*it.Change)
			case it.Event != nil:
				a.onEvent(*it.Event)
			}
		}
		if a.ctx.Err() == nil {
			a.setOffline(true)
		}
	}
}

func (a *App) onChange(ch core.Change) {
	switch ch.Kind {
	case core.ChangeStatus, core.ChangeActive, core.ChangeVPN, core.ChangeMonitor:
		a.refreshHeader()
	}
	for _, s := range changeDeps[ch.Kind] {
		a.views[s].refresh()
	}
	if a.tray != nil && (ch.Kind == core.ChangeVPN || ch.Kind == core.ChangeStatus) {
		a.tray.refresh()
	}
}

func (a *App) onEvent(e core.Event) {
	if e.Type == core.EventWifiScan || e.Type == core.EventStateChanged {
		return
	}
	if e.Type == core.EventSecretNeeded || e.Type == core.EventSecretResolved {
		a.onSecretEvent(e)
	}
	a.eventsMu.Lock()
	a.events = append(a.events, e)
	if len(a.events) > maxRecentEvents {
		a.events = a.events[len(a.events)-maxRecentEvents:]
	}
	recent := append([]core.Event(nil), a.events...)
	a.eventsMu.Unlock()
	a.refreshHeader()
	a.bg(func(context.Context) {
		a.onUI(func() { a.views[SectionOverview].(*overviewView).setEvents(recent) })
	})
}

func (a *App) offlineNow() bool {
	a.eventsMu.Lock()
	defer a.eventsMu.Unlock()
	return a.offline
}

func (a *App) setOffline(off bool) {
	a.eventsMu.Lock()
	changed := a.offline != off
	a.offline = off
	a.eventsMu.Unlock()
	if !changed {
		return
	}
	a.bg(func(context.Context) {
		a.onUI(func() {
			if off {
				a.banner.show("Daemon unreachable, retrying")
			} else {
				a.banner.hide()
			}
		})
	})
}

// --- tray ---------------------------------------------------------------------------

func (a *App) setupTray() {
	desk, ok := a.fy.(desktop.App)
	if !ok {
		return
	}
	want := false
	if a.opts.Tray != nil {
		want = *a.opts.Tray
	} else {
		want = hasTrayHost()
	}
	if !want {
		return
	}
	a.tray = newTray(a, desk)
	a.tray.refresh()
}
