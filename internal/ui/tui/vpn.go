package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/core"
)

// vpnTab shows one row per VPN whatever its backend; Enter toggles it. Tailscale
// rows offer an exit-node picker and login; `a` imports a .conf/.ovpn file.
type vpnTab struct {
	vpns   []core.VPN
	err    error
	loaded bool
	cursor int
	busy   string // VPN ID with a request in flight

	picker   *exitPicker
	addInput textinput.Model
	adding   bool
	loginURL string
	lastErr  error
}

type exitPicker struct {
	peers  []core.TailscalePeer
	cursor int
}

func (t *vpnTab) init() {
	t.addInput = newInput("file: ", "/path/to/tunnel.conf or .ovpn", 512)
}

func (t *vpnTab) load(m *Model) tea.Cmd { return m.l.vpns() }

func (t *vpnTab) capturing() bool { return t.adding || t.picker != nil }

// upCount is (connected, total) for the status bar.
func (t *vpnTab) upCount() (int, int) {
	up := 0
	for _, v := range t.vpns {
		if v.State == core.VPNConnected {
			up++
		}
	}
	return up, len(t.vpns)
}

func (t *vpnTab) selected() *core.VPN {
	if len(t.vpns) == 0 {
		return nil
	}
	t.cursor = min(max(t.cursor, 0), len(t.vpns)-1)
	return &t.vpns[t.cursor]
}

func (t *vpnTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case vpnMsg:
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.vpns = msg.vpns
		}
	case actionMsg:
		t.busy = ""
		if msg.err != nil {
			t.lastErr = msg.err
			return nil
		}
		t.lastErr = nil
		if msg.what == "login" && msg.extra != "" {
			t.loginURL = msg.extra
		}
		return m.l.vpns()
	case tea.KeyMsg:
		return t.key(m, msg)
	}
	return nil
}

func (t *vpnTab) key(m *Model, k tea.KeyMsg) tea.Cmd {
	t.lastErr = nil
	if t.picker != nil {
		return t.pickerKey(m, k)
	}
	if t.adding {
		switch k.String() {
		case "esc":
			t.adding = false
			t.addInput.Blur()
			return nil
		case "enter":
			path := strings.TrimSpace(t.addInput.Value())
			if path == "" {
				return nil
			}
			t.adding = false
			t.addInput.Blur()
			t.addInput.SetValue("")
			return t.importFile(m, path)
		}
		var cmd tea.Cmd
		t.addInput, cmd = t.addInput.Update(k)
		return cmd
	}
	switch k.String() {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
		t.loginURL = ""
	case "down", "j":
		t.cursor = min(max(0, len(t.vpns)-1), t.cursor+1)
		t.loginURL = ""
	case "enter":
		return t.toggle(m)
	case "e":
		v := t.selected()
		if v == nil || v.Backend != core.BackendTailscale || v.Tailscale == nil {
			m.over = overlayEvents
			return nil
		}
		var peers []core.TailscalePeer
		for _, p := range v.Tailscale.Peers {
			if p.ExitNodeOption {
				peers = append(peers, p)
			}
		}
		t.picker = &exitPicker{peers: peers}
	case "l":
		v := t.selected()
		if v == nil || v.Backend != core.BackendTailscale {
			return nil
		}
		t.busy = v.ID
		return tea.Batch(m.spin.Tick, m.l.actionWith(int(tabVPN), "login", func(ctx context.Context) (string, error) {
			return m.l.c.TailscaleLogin(ctx)
		}))
	case "a":
		t.adding = true
		return t.addInput.Focus()
	case "esc":
		t.loginURL = ""
	}
	return nil
}

func (t *vpnTab) toggle(m *Model) tea.Cmd {
	v := t.selected()
	if v == nil || t.busy != "" { // one request at a time; a second Enter would undo the first
		return nil
	}
	switch v.State {
	case core.VPNNeedsSetup, core.VPNUnavailable:
		msg := v.Name + ": " + string(v.State)
		if v.Detail != "" {
			msg += " — " + v.Detail
		}
		m.setFlash(msg, true)
		return m.flashTimer()
	case core.VPNConnected, core.VPNConnecting:
		t.busy = v.ID
		id := v.ID
		return tea.Batch(m.spin.Tick, m.l.action(int(tabVPN), "disconnect "+v.Name, func(ctx context.Context) error {
			return m.l.c.DisconnectVPN(ctx, id)
		}))
	default:
		t.busy = v.ID
		id := v.ID
		return tea.Batch(m.spin.Tick, m.l.action(int(tabVPN), "connect "+v.Name, func(ctx context.Context) error {
			return m.l.c.ConnectVPN(ctx, id)
		}))
	}
}

// importFile reads a .conf/.ovpn here and sends its content inline, like the
// CLI does: the daemon may run as a service with another working directory
// (so a relative path would not resolve there) and a leading ~ is only the
// shell's convention.
func (t *vpnTab) importFile(m *Model, path string) tea.Cmd {
	kind := ""
	switch strings.ToLower(filepath.Ext(path)) {
	case ".conf":
		kind = "wireguard"
	case ".ovpn":
		kind = "openvpn"
	default:
		m.setFlash("add: expected a .conf (WireGuard) or .ovpn (OpenVPN) file", true)
		return m.flashTimer()
	}
	path = expandHome(path)
	data, err := os.ReadFile(path)
	if err != nil {
		m.setFlash("add: "+err.Error(), true)
		return m.flashTimer()
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	req := api.ImportVPNRequest{Kind: kind, Name: name, Content: string(data)}
	return m.l.action(int(tabVPN), "import "+filepath.Base(path), func(ctx context.Context) error {
		_, err := m.l.c.ImportVPN(ctx, req)
		return err
	})
}

// expandHome replaces a leading ~/ with the home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func (t *vpnTab) pickerKey(m *Model, k tea.KeyMsg) tea.Cmd {
	p := t.picker
	switch k.String() {
	case "esc", "q":
		t.picker = nil
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = min(max(0, len(p.peers)-1), p.cursor+1)
	case "o":
		t.picker = nil
		return m.l.action(int(tabVPN), "exit node off", func(ctx context.Context) error {
			return m.l.c.SetTailscaleExitNodeEnabled(ctx, false)
		})
	case "enter":
		if p.cursor >= len(p.peers) {
			return nil
		}
		peer := p.peers[p.cursor]
		t.picker = nil
		return m.l.action(int(tabVPN), "exit node "+peer.Name, func(ctx context.Context) error {
			return m.l.c.SetTailscaleExitNode(ctx, peer.ID, false)
		})
	}
	return nil
}

func (t *vpnTab) hints(m *Model) string {
	switch {
	case t.picker != nil:
		return keyHints("enter", "use exit node", "o", "exit node off", "esc", "cancel")
	case t.adding:
		return keyHints("enter", "import", "esc", "cancel")
	}
	v := t.selected()
	if v != nil && (v.State == core.VPNNeedsSetup || v.State == core.VPNUnavailable) && v.Detail != "" {
		return stWarn.Render(truncate(v.Detail, 60))
	}
	h := keyHints("enter", "toggle", "a", "add file")
	if v != nil && v.Backend == core.BackendTailscale {
		h += "  " + keyHints("e", "exit node", "l", "login")
	}
	return h
}

func vpnDot(s core.VPNState) string {
	switch s {
	case core.VPNConnected:
		return stGood.Render("●")
	case core.VPNConnecting:
		return stWarn.Render("◐")
	case core.VPNError:
		return stBad.Render("✗")
	case core.VPNNeedsAuth:
		return stWarn.Render("?")
	case core.VPNNeedsSetup, core.VPNUnavailable:
		return stDim.Render("·")
	}
	return stDim.Render("○")
}

func (t *vpnTab) view(m *Model, w, h int) string {
	lines := []string{fit(stTitle.Render("VPN"), w)}
	if t.err != nil {
		lines = append(lines, stBad.Render(truncate("error: "+errText(t.err), w)))
	}
	if t.cursor >= len(t.vpns) {
		t.cursor = max(0, len(t.vpns)-1)
	}
	cells := make([][]string, 0, len(t.vpns))
	for _, v := range t.vpns {
		dot := vpnDot(v.State)
		if v.ID == t.busy {
			dot = m.spin.View()
		}
		detail := vpnDetail(v)
		name, kind, state := v.Name, v.Kind, string(v.State)
		if v.State == core.VPNNeedsSetup || v.State == core.VPNUnavailable {
			name, kind, detail = stDim.Render(name), stDim.Render(kind), stDim.Render(detail)
		}
		cells = append(cells, []string{dot, name, kind, stateStyle(state).Render(state), detail})
	}
	if len(cells) == 0 {
		msg := "no VPNs; press a to import a .conf or .ovpn"
		if !t.loaded {
			msg = "loading…"
		}
		lines = append(lines, stDim.Render(msg))
	} else {
		lines = append(lines, table(w, []int{1, 18, 12, 12, 0}, []string{"", "name", "kind", "state", "detail"}, cells, t.cursor))
	}
	if t.loginURL != "" {
		lines = append(lines, "", stBold.Render("open to log in: ")+stAccent.Render(t.loginURL))
	}
	if t.lastErr != nil {
		lines = append(lines, "", stBad.Render(truncate("✗ "+errText(t.lastErr), w)))
	}
	if t.adding {
		lines = append(lines, "", stBox.Width(min(w-4, 70)).Render(stBold.Render("add VPN from file")+"\n"+t.addInput.View()))
	}
	if p := t.picker; p != nil {
		var pl []string
		pl = append(pl, stBold.Render("exit node"))
		if len(p.peers) == 0 {
			pl = append(pl, stDim.Render("no peer offers an exit node"))
		}
		for i, peer := range p.peers {
			on := "○"
			if !peer.Online {
				on = stDim.Render("○")
			} else {
				on = stGood.Render("●")
			}
			row := fmt.Sprintf("%s %s %s", on, pad(peer.Name, 24), stDim.Render(strings.Join(peer.IPs, " ")))
			if peer.ExitNode {
				row += " " + stAccent.Render("(current)")
			}
			if i == p.cursor {
				row = stSelected.Render("› ") + row
			} else {
				row = "  " + row
			}
			pl = append(pl, row)
		}
		lines = append(lines, "", stBox.Width(min(w-4, 70)).Render(strings.Join(pl, "\n")))
	}
	return strings.Join(lines, "\n")
}

// vpnDetail is the one-line context for a VPN row.
func vpnDetail(v core.VPN) string {
	if v.Error != "" {
		return v.Error
	}
	switch {
	case v.Tailscale != nil:
		ts := v.Tailscale
		var parts []string
		if ts.SelfName != "" {
			parts = append(parts, ts.SelfName)
		}
		if len(ts.SelfIPs) > 0 {
			parts = append(parts, ts.SelfIPs[0])
		}
		if ts.ExitNodeOn && ts.ExitNodeName != "" {
			parts = append(parts, "exit "+ts.ExitNodeName)
		}
		if v.Detail != "" && len(parts) == 0 {
			return v.Detail
		}
		return strings.Join(parts, " ")
	case v.WireGuard != nil:
		wg := v.WireGuard
		var parts []string
		if wg.InterfaceName != "" {
			parts = append(parts, wg.InterfaceName)
		}
		if len(wg.Peers) > 0 && wg.Peers[0].Endpoint != "" {
			parts = append(parts, wg.Peers[0].Endpoint)
		}
		if v.Detail != "" && len(parts) == 0 {
			return v.Detail
		}
		return strings.Join(parts, " ")
	case v.NMVPN != nil:
		if v.NMVPN.Gateway != "" {
			return v.NMVPN.Gateway
		}
	}
	return v.Detail
}
