// Package tui is bnm's terminal UI: the nmtui replacement, a single Bubble Tea
// program with a left rail of tabs (Wi-Fi, Devices, VPN, Monitor, Speed, Diag),
// a status bar, and a footer with key hints and the last event. It talks to
// bnmd only through internal/client, never blocks the UI on a request (every
// load is a tea.Cmd) and refreshes live from the daemon's event stream,
// reconnecting with backoff when it drops.
//
// Tested by driving the model's Update/View in-process against a real
// internal/api server over a temp Unix socket backed by internal/fake, plus
// one teatest run of the whole program for the live stream.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// Run starts the TUI on the terminal and returns when the user quits or ctx
// ends. It is the hook the CLI calls for `bnm` with no arguments.
func Run(ctx context.Context, c *client.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := New(ctx, c)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

type tab int

const (
	tabWifi tab = iota
	tabDevices
	tabVPN
	tabMonitor
	tabSpeed
	tabDiag
	tabCount
)

var tabNames = [tabCount]string{"Wi-Fi", "Devices", "VPN", "Monitor", "Speed", "Diag"}

type overlay int

const (
	overlayNone overlay = iota
	overlayHelp
	overlayEvents
)

// Minimum terminal size below which the layout gives up and says so.
const (
	minWidth  = 40
	minHeight = 8
)

// pane is what every tab implements.
type pane interface {
	// load (re)reads the tab's data.
	load(m *Model) tea.Cmd
	// update handles keys and data messages; it returns nil for keys it ignores.
	update(m *Model, msg tea.Msg) tea.Cmd
	// view renders into exactly w x h cells.
	view(m *Model, w, h int) string
	// hints is the footer key legend.
	hints(m *Model) string
	// capturing says a text input has focus, so global keys must not fire.
	capturing() bool
}

// Model is the whole TUI. It is a pointer model: Update mutates in place.
type Model struct {
	l      loader
	width  int
	height int
	tab    tab
	over   overlay

	status    api.StatusResponse
	statusErr error
	st        stream

	events    []core.Event
	lastEvent *core.Event

	flash     flash
	flashID   int
	spin      spinner.Model
	eventsTop int // scroll offset of the events overlay

	wifi    wifiTab
	devices devicesTab
	vpn     vpnTab
	monitor monitorTab
	speed   speedTab
	diag    diagTab

	now      func() time.Time
	flashTTL time.Duration
}

// cursorMode is applied to every text input; tests make it static so no blink
// timers run.
var cursorMode = cursor.CursorBlink

// newInput builds a text input with the shared cursor mode.
func newInput(prompt, placeholder string, limit int) textinput.Model {
	in := textinput.New()
	in.Prompt = prompt
	in.Placeholder = placeholder
	in.CharLimit = limit
	in.Cursor.SetMode(cursorMode)
	return in
}

type flash struct {
	text  string
	isErr bool
}

type flashExpireMsg struct{ id int }

// New builds the model; ctx bounds every request and the event stream.
func New(ctx context.Context, c *client.Client) *Model {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(stAccent))
	m := &Model{
		l:        loader{c: c, ctx: ctx},
		spin:     sp,
		now:      time.Now,
		flashTTL: 6 * time.Second,
	}
	m.wifi.init()
	m.vpn.init()
	m.diag.init()
	return m
}

func (m *Model) pane(t tab) pane {
	switch t {
	case tabWifi:
		return &m.wifi
	case tabDevices:
		return &m.devices
	case tabVPN:
		return &m.vpn
	case tabMonitor:
		return &m.monitor
	case tabSpeed:
		return &m.speed
	default:
		return &m.diag
	}
}

// Init loads the summaries every tab needs, the first tab, the event log and
// opens the stream.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.l.status(),
		m.l.vpns(),
		m.l.monitor(),
		m.l.eventHistory(),
		m.pane(m.tab).load(m),
		m.l.openStream(),
	)
}

// Update is the single message loop.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m, m.key(msg)
	case spinner.TickMsg:
		if !m.busy() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case flashExpireMsg:
		if msg.id == m.flashID {
			m.flash = flash{}
		}
		return m, nil

	case statusMsg:
		m.statusErr = msg.err
		if msg.err == nil {
			m.status = msg.st
		}
		return m, nil
	case eventHistoryMsg:
		if msg.err == nil {
			m.events = mergeEvents(msg.events, m.events)
			if m.lastEvent == nil && len(m.events) > 0 {
				m.lastEvent = &m.events[len(m.events)-1]
			}
		}
		return m, nil

	case streamOpenMsg:
		reconnect := m.st.everUp
		m.st.ch = msg.ch
		m.st.up, m.st.everUp, m.st.backoff, m.st.lastErr = true, true, 0, nil
		if reconnect { // things may have moved while we were away
			return m, tea.Batch(waitStream(msg.ch), m.reloadAll())
		}
		return m, waitStream(msg.ch)
	case streamErrMsg:
		m.st.up, m.st.lastErr = false, msg.err
		return m, retryStream(m.st.nextBackoff())
	case streamClosedMsg:
		m.st.up, m.st.ch = false, nil
		return m, retryStream(m.st.nextBackoff())
	case streamRetryMsg:
		return m, m.l.openStream()
	case streamItemMsg:
		cmd := m.onStreamItem(msg.item)
		return m, tea.Batch(waitStream(m.st.ch), cmd)
	case debounceMsg:
		m.st.debouncin = false
		return m, m.loadsFor(m.st.take())

	case actionMsg:
		if msg.err != nil {
			m.setFlash(msg.what+": "+errText(msg.err), true)
		}
		cmd := m.pane(tab(msg.tab)).update(m, msg)
		return m, tea.Batch(cmd, m.flashTimer())
	}
	// Data messages go to their owners; some feed two tabs.
	var cmds []tea.Cmd
	switch msg.(type) {
	case wifiMsg:
		cmds = append(cmds, m.wifi.update(m, msg))
	case devicesMsg, profilesMsg:
		cmds = append(cmds, m.devices.update(m, msg))
	case vpnMsg:
		cmds = append(cmds, m.vpn.update(m, msg))
	case monitorMsg, samplesMsg:
		cmds = append(cmds, m.monitor.update(m, msg))
	case speedHistoryMsg, speedProgressMsg, speedDoneMsg:
		cmds = append(cmds, m.speed.update(m, msg))
	case lanMsg, portsMsg, routesMsg, infraMsg, publicIPMsg, dnsMsg:
		cmds = append(cmds, m.diag.update(m, msg))
	default:
		cmds = append(cmds, m.pane(m.tab).update(m, msg))
	}
	return m, tea.Batch(cmds...)
}

// key routes a key press: overlays first, then a focused input, then the
// global bindings, then the tab.
func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	if k.Type == tea.KeyCtrlC {
		return tea.Quit
	}
	if m.over != overlayNone {
		switch k.String() {
		case "esc", "q", "?", "e", "E", "enter":
			m.over = overlayNone
			m.eventsTop = 0
		case "up", "k":
			m.eventsTop = max(0, m.eventsTop-1)
		case "down", "j":
			m.eventsTop++
		}
		return nil
	}
	p := m.pane(m.tab)
	if p.capturing() {
		return p.update(m, k)
	}
	switch k.String() {
	case "q":
		if m.tab != tabSpeed { // the Speed tab uses q for a quick test
			return tea.Quit
		}
	case "?":
		m.over = overlayHelp
		return nil
	case "E":
		m.over = overlayEvents
		return nil
	case "e":
		if m.tab != tabVPN { // the VPN tab uses e for exit nodes
			m.over = overlayEvents
			return nil
		}
	case "tab":
		return m.switchTab((m.tab + 1) % tabCount)
	case "shift+tab":
		return m.switchTab((m.tab + tabCount - 1) % tabCount)
	case "1", "2", "3", "4", "5", "6":
		return m.switchTab(tab(k.String()[0] - '1'))
	}
	return p.update(m, k)
}

func (m *Model) switchTab(t tab) tea.Cmd {
	if t == m.tab {
		return nil
	}
	m.tab = t
	return m.pane(t).load(m)
}

// busy says whether any spinner is showing.
func (m *Model) busy() bool {
	return m.wifi.connecting != "" || m.vpn.busy != "" || m.speed.running || m.diag.sweeping
}

func (m *Model) setFlash(text string, isErr bool) {
	m.flashID++
	m.flash = flash{text: text, isErr: isErr}
}

func (m *Model) flashTimer() tea.Cmd {
	if m.flash.text == "" {
		return nil
	}
	id := m.flashID
	return tea.Tick(m.flashTTL, func(time.Time) tea.Msg { return flashExpireMsg{id} })
}

// onStreamItem folds one live item in: Changes are coalesced, Events are
// logged and mapped to the data they invalidate.
func (m *Model) onStreamItem(item client.StreamItem) tea.Cmd {
	if c := item.Change; c != nil {
		m.st.note(c.Kind)
	}
	if e := item.Event; e != nil {
		ev := *e
		m.events = append(m.events, ev)
		if len(m.events) > eventLogSize {
			m.events = m.events[len(m.events)-eventLogSize:]
		}
		m.lastEvent = &m.events[len(m.events)-1]
		switch e.Type {
		case core.EventConnected, core.EventDisconnected, core.EventNoInternet, core.EventInternetRestored, core.EventStateChanged:
			m.st.note(core.ChangeStatus)
			m.st.note(core.ChangeActive)
		case core.EventVPNUp, core.EventVPNDown:
			m.st.note(core.ChangeVPN)
		case core.EventDegraded, core.EventRecovered:
			m.st.note(core.ChangeMonitor)
		case core.EventWifiScan:
			m.st.note(core.ChangeWifi)
		}
	}
	if len(m.st.pending) > 0 && !m.st.debouncin {
		m.st.debouncin = true
		return debounce()
	}
	return nil
}

// loadsFor maps coalesced change kinds to the minimal set of reloads.
func (m *Model) loadsFor(kinds map[core.ChangeKind]struct{}) tea.Cmd {
	want := map[string]bool{}
	for k := range kinds {
		switch k {
		case core.ChangeStatus:
			want["status"] = true
		case core.ChangeWifi:
			want["wifi"] = true
		case core.ChangeActive:
			want["status"], want["wifi"], want["devices"] = true, true, true
		case core.ChangeDevices:
			want["devices"] = true
		case core.ChangeProfiles:
			want["wifi"], want["devices"], want["profiles"] = true, true, true
		case core.ChangeVPN:
			want["vpn"] = true
		case core.ChangeMonitor:
			want["monitor"] = true
		default:
			want["status"] = true
		}
	}
	var cmds []tea.Cmd
	if want["status"] {
		cmds = append(cmds, m.l.status())
	}
	if want["wifi"] && (m.tab == tabWifi || m.wifi.loaded) {
		cmds = append(cmds, m.l.wifi())
	}
	if want["devices"] && (m.tab == tabDevices || m.devices.loaded) {
		cmds = append(cmds, m.l.devices())
	}
	if want["profiles"] && m.tab == tabDevices {
		cmds = append(cmds, m.l.profiles())
	}
	if want["vpn"] {
		cmds = append(cmds, m.l.vpns())
	}
	if want["monitor"] {
		if m.tab == tabMonitor {
			cmds = append(cmds, m.monitor.load(m))
		} else {
			cmds = append(cmds, m.l.monitor())
		}
	}
	return tea.Batch(cmds...)
}

// reloadAll refreshes the summaries and the current tab (after a reconnect).
func (m *Model) reloadAll() tea.Cmd {
	return tea.Batch(m.l.status(), m.l.vpns(), m.l.monitor(), m.pane(m.tab).load(m))
}

func (s *stream) nextBackoff() time.Duration {
	if s.backoff == 0 {
		s.backoff = backoffMin
	} else {
		s.backoff = min(s.backoff*2, backoffMax)
	}
	return s.backoff
}

// mergeEvents returns history followed by any live events not already in it,
// capped to the log size.
func mergeEvents(history, live []core.Event) []core.Event {
	out := append([]core.Event(nil), history...)
	seen := map[string]bool{}
	for _, e := range history {
		seen[e.Time.String()+string(e.Type)] = true
	}
	for _, e := range live {
		if !seen[e.Time.String()+string(e.Type)] {
			out = append(out, e)
		}
	}
	if len(out) > eventLogSize {
		out = out[len(out)-eventLogSize:]
	}
	return out
}

// --- view ------------------------------------------------------------------

// View composes status bar, rail + pane, and footer to exactly the terminal size.
func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading…"
	}
	if m.width < minWidth || m.height < minHeight {
		return fmt.Sprintf("bnm needs at least %dx%d (have %dx%d)", minWidth, minHeight, m.width, m.height)
	}
	bodyH := m.height - 2
	var body string
	switch m.over {
	case overlayHelp:
		body = m.helpView(m.width, bodyH)
	case overlayEvents:
		body = m.eventsView(m.width, bodyH)
	default:
		railW := 12
		if m.width < 80 {
			railW = 3
		}
		paneW := m.width - railW - 1
		rail := m.railView(railW, bodyH)
		sep := strings.TrimRight(strings.Repeat(stDim.Render("│")+"\n", bodyH), "\n")
		p := clampBox(m.pane(m.tab).view(m, paneW, bodyH), paneW, bodyH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, rail, sep, p)
	}
	return m.statusBar() + "\n" + clampLines(body, bodyH) + "\n" + m.footer()
}

func (m *Model) railView(w, h int) string {
	lines := make([]string, 0, h)
	for i := tab(0); i < tabCount; i++ {
		label := fmt.Sprintf("%d %s", i+1, tabNames[i])
		if w < 8 {
			label = fmt.Sprintf("%d", i+1)
		}
		if i == m.tab {
			lines = append(lines, fit(stRailOn.Render(pad(label, w)), w))
		} else {
			lines = append(lines, fit(stRailOff.Render(pad(label, w)), w))
		}
	}
	return clampLines(strings.Join(lines, "\n"), h)
}

func (m *Model) statusBar() string {
	var parts []string
	parts = append(parts, stBarAccent.Render(" bnm "))
	switch {
	case m.statusErr != nil:
		parts = append(parts, stBarBad.Render("daemon unreachable"))
	case m.status.Primary != nil:
		p := m.status.Primary
		conn := p.ProfileName
		if ip := firstIP(p.IPv4); ip != "" {
			conn += " " + ip
		}
		parts = append(parts, stBar.Render(conn))
	default:
		parts = append(parts, stBarDim.Render("not connected"))
	}
	dot, st := "○", stBarDim
	switch m.status.Connectivity {
	case core.ConnFull:
		dot, st = "●", stBarGood
	case core.ConnLimited, core.ConnPortal:
		dot, st = "◐", stBarWarn
	case core.ConnNone:
		dot, st = "○", stBarBad
	}
	conn := string(m.status.Connectivity)
	if conn == "" {
		conn = "unknown"
	}
	parts = append(parts, st.Render(dot+" "+conn))
	up, total := m.vpn.upCount()
	vpnSt := stBarDim
	if up > 0 {
		vpnSt = stBarGood
	}
	parts = append(parts, vpnSt.Render(fmt.Sprintf("vpn %d/%d", up, total)))
	mon := string(m.monitor.status.State)
	if mon == "" {
		mon = "idle"
	}
	if m.monitor.status.Paused {
		mon = "paused"
	}
	parts = append(parts, stBar.Render("monitor ")+barStateStyle(mon).Render(mon))
	left := strings.Join(parts, stBar.Render("  "))

	var right string
	switch {
	case m.st.up:
		right = stBarDim.Render("bnmd " + m.status.Version + " ")
	case m.st.everUp || m.st.lastErr != nil:
		right = stBarWarn.Render("daemon: reconnecting… ")
	default:
		right = stBarDim.Render("connecting… ")
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, m.width)
	}
	return left + stBar.Render(strings.Repeat(" ", gap)) + right
}

func barStateStyle(s string) lipgloss.Style {
	switch s {
	case "ok":
		return stBarGood
	case "learning", "paused":
		return stBarWarn
	case "degraded":
		return stBarBad
	}
	return stBarDim
}

func (m *Model) footer() string {
	hints := m.pane(m.tab).hints(m)
	if m.over != overlayNone {
		hints = keyHints("esc", "close", "j/k", "scroll")
	}
	globals := keyHints("?", "help", "e", "events", "q", "quit")
	if m.tab == tabSpeed {
		globals = keyHints("?", "help", "e", "events", "ctrl-c", "quit")
	}
	if m.tab == tabVPN {
		globals = keyHints("?", "help", "E", "events", "q", "quit")
	}
	left := " " + hints + "  " + globals
	var right string
	switch {
	case m.flash.text != "":
		st := stBarAccent
		if m.flash.isErr {
			st = stBarBad
		}
		right = st.Render(m.flash.text + " ")
	case m.lastEvent != nil:
		right = stBarDim.Render(clock(m.lastEvent.Time)+" ") + stBar.Render(m.lastEvent.Title+" ")
	}
	rw := lipgloss.Width(right)
	maxRight := m.width / 2
	if rw > maxRight {
		right = truncate(right, maxRight)
		rw = lipgloss.Width(right)
	}
	leftW := m.width - rw
	left = fit(truncate(left, leftW), leftW)
	return stBar.Render(left) + right
}

func (m *Model) helpView(w, h int) string {
	sec := func(name string) string { return stBold.Render(pad(name, 8)) }
	rows := []string{
		stTitle.Render("bnm keys"),
		"",
		sec("global") + keyHints("1-6", "tabs", "tab / shift-tab", "next / prev tab", "?", "this help"),
		sec("") + keyHints("e / E", "event log", "q / ctrl-c", "quit"),
		"",
		sec("Wi-Fi") + keyHints("enter", "connect", "d", "disconnect", "f", "forget", "r", "rescan"),
		sec("") + keyHints("w", "wifi on/off", "/", "filter", "esc", "clear filter"),
		sec("Devices") + keyHints("enter", "wired up/down", "x", "expand virtual"),
		sec("VPN") + keyHints("enter", "connect/disconnect", "a", "add .conf/.ovpn"),
		sec("") + keyHints("e", "exit node (Tailscale)", "l", "login", "E", "event log"),
		sec("Monitor") + keyHints("p", "pause/resume", "R", "reset baseline", "r", "reload"),
		sec("Speed") + keyHints("s", "full test", "q", "quick test", "esc", "cancel"),
		sec("Diag") + keyHints("[ / ]", "sub-view", "s", "LAN sweep", "r", "reload", "enter", "dns box"),
		"",
		stDim.Render("esc closes this"),
	}
	inner := min(w-6, 72)
	for i := range rows {
		rows[i] = truncate(rows[i], inner)
	}
	box := stBox.Width(inner).Render(strings.Join(rows, "\n"))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

func (m *Model) eventsView(w, h int) string {
	inner := min(w-4, 100)
	lines := []string{stTitle.Render(fmt.Sprintf("events (%d)", len(m.events)))}
	if len(m.events) == 0 {
		lines = append(lines, stDim.Render("nothing yet"))
	}
	avail := max(1, h-4)
	// newest first
	evs := make([]core.Event, len(m.events))
	for i, e := range m.events {
		evs[len(m.events)-1-i] = e
	}
	if m.eventsTop > max(0, len(evs)-avail) {
		m.eventsTop = max(0, len(evs)-avail)
	}
	end := min(len(evs), m.eventsTop+avail)
	for _, e := range evs[m.eventsTop:end] {
		line := stDim.Render(clock(e.Time)) + " " + stateStyle(eventState(e.Type)).Render(pad(string(e.Type), 17)) + " " + e.Title
		if e.Body != "" {
			line += " " + stDim.Render(e.Body)
		}
		lines = append(lines, truncate(line, inner-2))
	}
	box := stBox.Width(inner).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

func eventState(t core.EventType) string {
	switch t {
	case core.EventConnected, core.EventInternetRestored, core.EventVPNUp, core.EventRecovered:
		return "ok"
	case core.EventDisconnected, core.EventNoInternet, core.EventVPNDown, core.EventDegraded:
		return "error"
	}
	return ""
}
