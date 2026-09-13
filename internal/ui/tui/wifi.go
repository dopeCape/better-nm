package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/core"
)

// wifiTab lists networks (active first, then by signal) and drives connect,
// disconnect, forget, rescan and the Wi-Fi radio. Unknown secured networks
// prompt inline for a password (and a username for EAP).
type wifiTab struct {
	nets   []core.WifiNetwork
	err    error
	loaded bool
	cursor int

	filter    textinput.Model
	filtering bool

	prompt     *wifiPrompt
	connecting string // SSID being joined
	confirm    *core.WifiNetwork
	lastErr    error
}

type wifiPrompt struct {
	net    core.WifiNetwork
	fields []textinput.Model // [username,] password
	focus  int
}

func (t *wifiTab) init() {
	t.filter = newInput("/ ", "filter", 64)
}

func (t *wifiTab) load(m *Model) tea.Cmd { return m.l.wifi() }

func (t *wifiTab) capturing() bool { return t.filtering || t.prompt != nil || t.confirm != nil }

// visible applies the filter and the sort order.
func (t *wifiTab) visible() []core.WifiNetwork {
	q := strings.ToLower(t.filter.Value())
	out := make([]core.WifiNetwork, 0, len(t.nets))
	for _, n := range t.nets {
		if q == "" || strings.Contains(strings.ToLower(n.SSID), q) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if out[i].Strength != out[j].Strength {
			return out[i].Strength > out[j].Strength
		}
		return out[i].SSID < out[j].SSID
	})
	return out
}

func (t *wifiTab) selected() *core.WifiNetwork {
	v := t.visible()
	if len(v) == 0 {
		return nil
	}
	t.cursor = min(max(t.cursor, 0), len(v)-1)
	n := v[t.cursor]
	return &n
}

func (t *wifiTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case wifiMsg:
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.nets = msg.nets
			if t.connecting != "" {
				for _, n := range msg.nets {
					if n.SSID == t.connecting && n.Active {
						t.connecting = ""
					}
				}
			}
		}
		return nil
	case actionMsg:
		if strings.HasPrefix(msg.what, "connect ") {
			t.connecting = ""
		}
		if msg.err != nil {
			t.lastErr = msg.err
			return nil
		}
		t.lastErr = nil
		return m.l.wifi()
	case tea.KeyMsg:
		return t.key(m, msg)
	}
	return nil
}

func (t *wifiTab) key(m *Model, k tea.KeyMsg) tea.Cmd {
	t.lastErr = nil
	if t.prompt != nil {
		return t.promptKey(m, k)
	}
	if t.confirm != nil {
		n := *t.confirm
		t.confirm = nil
		if k.String() == "y" || k.String() == "Y" {
			return t.forget(m, n)
		}
		return nil
	}
	if t.filtering {
		switch k.String() {
		case "esc":
			t.filtering = false
			t.filter.SetValue("")
			t.filter.Blur()
			return nil
		case "enter":
			t.filtering = false
			t.filter.Blur()
			return nil
		}
		var cmd tea.Cmd
		t.filter, cmd = t.filter.Update(k)
		t.cursor = 0
		return cmd
	}
	switch k.String() {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		if n := len(t.visible()); n > 0 {
			t.cursor = min(n-1, t.cursor+1)
		}
	case "g", "home":
		t.cursor = 0
	case "G", "end":
		t.cursor = max(0, len(t.visible())-1)
	case "/":
		t.filtering = true
		return t.filter.Focus()
	case "esc":
		if t.filter.Value() != "" {
			t.filter.SetValue("")
			t.cursor = 0
		}
	case "enter":
		return t.connect(m)
	case "d":
		return m.l.action(int(tabWifi), "disconnect", func(ctx context.Context) error {
			return m.l.c.DisconnectWifi(ctx, t.device())
		})
	case "f":
		if n := t.selected(); n != nil && n.Known {
			t.confirm = n
		} else if n != nil {
			m.setFlash(n.SSID+" is not a known network", false)
			return m.flashTimer()
		}
	case "r":
		m.setFlash("scanning…", false)
		return tea.Batch(m.l.action(int(tabWifi), "rescan", func(ctx context.Context) error {
			return m.l.c.ScanWifi(ctx, t.device())
		}), m.flashTimer())
	case "w":
		on := !m.status.WifiEnabled
		what := "wifi off"
		if on {
			what = "wifi on"
		}
		return m.l.action(int(tabWifi), what, func(ctx context.Context) error {
			return m.l.c.SetWifiEnabled(ctx, on)
		})
	}
	return nil
}

// device is the Wi-Fi device of the selected row, or "" for the first one.
func (t *wifiTab) device() string {
	if n := t.selected(); n != nil {
		return n.Device
	}
	return ""
}

func (t *wifiTab) connect(m *Model) tea.Cmd {
	n := t.selected()
	if n == nil {
		return nil
	}
	if n.Active {
		m.setFlash("already connected to "+n.SSID, false)
		return m.flashTimer()
	}
	if !n.Known && secured(n.Security) {
		p := &wifiPrompt{net: *n}
		if n.Security == core.SecWPAEAP {
			p.fields = append(p.fields, newInput("username: ", "", 128))
		}
		pw := newInput("password: ", "", 128)
		pw.EchoMode = textinput.EchoPassword
		pw.EchoCharacter = '•'
		p.fields = append(p.fields, pw)
		t.prompt = p
		return p.fields[0].Focus()
	}
	return t.join(m, core.ConnectWifiRequest{Device: n.Device, SSID: n.SSID})
}

func (t *wifiTab) join(m *Model, req core.ConnectWifiRequest) tea.Cmd {
	t.connecting = req.SSID
	return tea.Batch(m.spin.Tick, m.l.action(int(tabWifi), "connect "+req.SSID, func(ctx context.Context) error {
		return m.l.c.ConnectWifi(ctx, req)
	}))
}

func (t *wifiTab) forget(m *Model, n core.WifiNetwork) tea.Cmd {
	return m.l.action(int(tabWifi), "forget "+n.SSID, func(ctx context.Context) error {
		return m.l.c.ForgetWifi(ctx, n.ProfileUUID)
	})
}

func (t *wifiTab) promptKey(m *Model, k tea.KeyMsg) tea.Cmd {
	p := t.prompt
	switch k.String() {
	case "esc":
		t.prompt = nil
		return nil
	case "tab", "shift+tab":
		p.fields[p.focus].Blur()
		p.focus = (p.focus + 1) % len(p.fields)
		return p.fields[p.focus].Focus()
	case "enter":
		if p.focus < len(p.fields)-1 {
			p.fields[p.focus].Blur()
			p.focus++
			return p.fields[p.focus].Focus()
		}
		req := core.ConnectWifiRequest{Device: p.net.Device, SSID: p.net.SSID, Hidden: p.net.Hidden}
		if len(p.fields) == 2 {
			req.Username = p.fields[0].Value()
			req.Password = p.fields[1].Value()
		} else {
			req.Password = p.fields[0].Value()
		}
		if req.Password == "" {
			return nil
		}
		t.prompt = nil
		return t.join(m, req)
	}
	var cmd tea.Cmd
	p.fields[p.focus], cmd = p.fields[p.focus].Update(k)
	return cmd
}

func (t *wifiTab) hints(m *Model) string {
	switch {
	case t.prompt != nil:
		return keyHints("enter", "join", "tab", "next field", "esc", "cancel")
	case t.confirm != nil:
		return keyHints("y", "forget "+t.confirm.SSID, "n", "keep")
	case t.filtering:
		return keyHints("type", "to filter", "enter", "done", "esc", "clear")
	}
	w := "wifi off"
	if !m.status.WifiEnabled {
		w = "wifi on"
	}
	return keyHints("enter", "connect", "d", "disconnect", "f", "forget", "r", "rescan", "w", w, "/", "filter")
}

func (t *wifiTab) view(m *Model, w, h int) string {
	var lines []string
	title := stTitle.Render("Wi-Fi")
	if m.status.WifiHardware && !m.status.WifiEnabled {
		title += "  " + stWarn.Render("radio off")
	}
	if t.filter.Value() != "" || t.filtering {
		title += "  " + t.filter.View()
	}
	lines = append(lines, fit(title, w))
	if t.err != nil {
		lines = append(lines, stBad.Render(truncate("error: "+errText(t.err), w)))
	}
	vis := t.visible()
	if t.cursor >= len(vis) {
		t.cursor = max(0, len(vis)-1)
	}
	rows := make([][]string, 0, len(vis))
	for _, n := range vis {
		sig := signalStyle(n.Strength).Render(signalBars(n.Strength)) + fmt.Sprintf(" %3d%%", n.Strength)
		band := ""
		if n.Band != "" {
			band = n.Band + " GHz"
		}
		if n.Channel > 0 {
			band += fmt.Sprintf(" ch%d", n.Channel)
		}
		sec := "  " + securityLabel(n.Security)
		if secured(n.Security) {
			sec = "🔒" + securityLabel(n.Security)
		}
		mark := ""
		switch {
		case n.SSID == t.connecting:
			mark = m.spin.View() + " connecting"
		case n.Active:
			mark = stGood.Render("● active")
		case n.Known:
			mark = stDim.Render("known")
		}
		ssid := n.SSID
		if n.Hidden {
			ssid += stDim.Render(" (hidden)")
		}
		rows = append(rows, []string{sig, ssid, band, sec, mark})
	}
	listH := h - len(lines) - 1
	if t.prompt != nil {
		listH -= 4
	}
	if t.confirm != nil || t.lastErr != nil {
		listH -= 2
	}
	listH = max(listH, 3)
	if len(rows) == 0 {
		msg := "no networks in range"
		if t.filter.Value() != "" {
			msg = "nothing matches " + t.filter.Value()
		}
		if !t.loaded {
			msg = "loading…"
		}
		lines = append(lines, stDim.Render(msg))
	} else {
		// window the rows around the cursor
		start := 0
		if t.cursor >= listH-1 {
			start = t.cursor - (listH - 1) + 1
		}
		end := min(len(rows), start+listH-1)
		win := rows[start:end]
		lines = append(lines, table(w, []int{9, 0, 12, 6, 14}, []string{"signal", "ssid", "band", "sec", ""}, win, t.cursor-start))
	}
	body := strings.Join(lines, "\n")
	if t.prompt != nil {
		p := t.prompt
		var fl []string
		fl = append(fl, stBold.Render("join "+p.net.SSID)+stDim.Render(" ("+securityLabel(p.net.Security)+")"))
		for _, f := range p.fields {
			fl = append(fl, f.View())
		}
		body += "\n\n" + stBox.Width(min(w-4, 60)).Render(strings.Join(fl, "\n"))
	}
	if t.confirm != nil {
		body += "\n\n" + stWarn.Render("forget "+t.confirm.SSID+"? (y/n)")
	}
	if t.lastErr != nil {
		body += "\n\n" + stBad.Render(truncate("✗ "+errText(t.lastErr), w))
	}
	return body
}
