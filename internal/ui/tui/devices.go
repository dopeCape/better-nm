package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/core"
)

// devicesTab shows physical devices first and the Infra ones collapsed under a
// "Virtual (n)" row with their owner tags. Enter on a wired device toggles it.
type devicesTab struct {
	devs     []core.Device
	profiles []core.Profile
	err      error
	loaded   bool
	cursor   int
	expanded bool
}

// devRow is one line of the flattened list.
type devRow struct {
	dev     *core.Device
	virtual bool // the "Virtual (n)" header
	child   bool // an expanded Infra device
}

func (t *devicesTab) load(m *Model) tea.Cmd { return tea.Batch(m.l.devices(), m.l.profiles()) }

func (t *devicesTab) capturing() bool { return false }

func (t *devicesTab) rows() []devRow {
	var phys, infra []*core.Device
	for i := range t.devs {
		d := &t.devs[i]
		switch d.Class {
		case core.ClassPhysical:
			phys = append(phys, d)
		case core.ClassLoopback:
		default:
			infra = append(infra, d)
		}
	}
	out := make([]devRow, 0, len(phys)+1+len(infra))
	for _, d := range phys {
		out = append(out, devRow{dev: d})
	}
	if len(infra) > 0 {
		out = append(out, devRow{virtual: true})
		if t.expanded {
			for _, d := range infra {
				out = append(out, devRow{dev: d, child: true})
			}
		}
	}
	return out
}

func (t *devicesTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case devicesMsg:
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.devs = msg.devs
		}
	case profilesMsg:
		if msg.err == nil {
			t.profiles = msg.profiles
		}
	case actionMsg:
		if msg.err == nil {
			return m.l.devices()
		}
	case tea.KeyMsg:
		return t.key(m, msg)
	}
	return nil
}

func (t *devicesTab) key(m *Model, k tea.KeyMsg) tea.Cmd {
	rows := t.rows()
	switch k.String() {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = min(max(0, len(rows)-1), t.cursor+1)
	case "x":
		t.expanded = !t.expanded
	case "enter":
		if t.cursor >= len(rows) {
			return nil
		}
		r := rows[t.cursor]
		if r.virtual {
			t.expanded = !t.expanded
			return nil
		}
		return t.toggle(m, *r.dev)
	}
	return nil
}

// toggle brings a wired device down when it is up, else activates the profile
// bound to it (or any wired profile).
func (t *devicesTab) toggle(m *Model, d core.Device) tea.Cmd {
	if d.Kind == core.DeviceWifi {
		return m.switchTab(tabWifi)
	}
	if d.Class != core.ClassPhysical {
		m.setFlash(d.Name+" is managed by "+ownerOf(d), false)
		return m.flashTimer()
	}
	switch d.State {
	case core.DeviceConnected, core.DeviceConnecting, core.DeviceExternal:
		return m.l.action(int(tabDevices), "down "+d.Name, func(ctx context.Context) error {
			return m.l.c.DisconnectWifi(ctx, d.Name) // POST /wifi/disconnect brings any device down
		})
	}
	uuid := ""
	for _, p := range t.profiles {
		if p.InterfaceName == d.Name {
			uuid = p.UUID
			break
		}
	}
	if uuid == "" {
		for _, p := range t.profiles {
			if p.Type == core.ProfileEthernet && p.InterfaceName == "" {
				uuid = p.UUID
				break
			}
		}
	}
	if uuid == "" {
		m.setFlash("no profile for "+d.Name+"; create one with nmcli or bnm", true)
		return m.flashTimer()
	}
	return m.l.action(int(tabDevices), "up "+d.Name, func(ctx context.Context) error {
		return m.l.c.ActivateProfile(ctx, uuid, d.Name)
	})
}

func ownerOf(d core.Device) string {
	if d.Owner != "" {
		return d.Owner
	}
	return string(d.Kind)
}

func (t *devicesTab) hints(m *Model) string {
	x := "expand virtual"
	if t.expanded {
		x = "collapse virtual"
	}
	return keyHints("enter", "wired up/down", "x", x)
}

func (t *devicesTab) view(m *Model, w, h int) string {
	lines := []string{fit(stTitle.Render("Devices"), w)}
	if t.err != nil {
		lines = append(lines, stBad.Render(truncate("error: "+errText(t.err), w)))
	}
	rows := t.rows()
	if t.cursor >= len(rows) {
		t.cursor = max(0, len(rows)-1)
	}
	cells := make([][]string, 0, len(rows))
	nInfra := 0
	for _, d := range t.devs {
		if d.Class == core.ClassInfra {
			nInfra++
		}
	}
	for _, r := range rows {
		if r.virtual {
			arrow := "▸"
			if t.expanded {
				arrow = "▾"
			}
			cells = append(cells, []string{stDim.Render(fmt.Sprintf("%s Virtual (%d)", arrow, nInfra)), "", "", "", ""})
			continue
		}
		d := r.dev
		name := d.Name
		if r.child {
			name = "  " + name
		}
		kind := string(d.Kind)
		if r.child && d.Owner != "" && d.Owner != kind {
			kind = d.Owner + "/" + kind
		}
		state := stateStyle(string(d.State)).Render(string(d.State))
		if !d.Managed {
			state = stDim.Render("unmanaged")
		}
		ip := joinIPs(d.IPv4)
		if ip == "" {
			ip = firstIP(d.IPv6)
		}
		prof := d.ActiveName
		if prof == "" && d.Speed > 0 && d.State == core.DeviceConnected {
			prof = fmt.Sprintf("%d Mb/s", d.Speed)
		}
		cells = append(cells, []string{name, kind, state, ip, prof})
	}
	if len(cells) == 0 {
		msg := "no devices"
		if !t.loaded {
			msg = "loading…"
		}
		lines = append(lines, stDim.Render(msg))
	} else {
		lines = append(lines, table(w, []int{16, 14, 12, 18, 0}, []string{"device", "kind", "state", "ip", "profile"}, cells, t.cursor))
	}
	if t.cursor < len(rows) && !rows[t.cursor].virtual {
		d := rows[t.cursor].dev
		var det []string
		if d.HwAddr != "" {
			det = append(det, "mac "+d.HwAddr)
		}
		if d.Driver != "" {
			det = append(det, "driver "+d.Driver)
		}
		if d.Gateway4 != "" {
			det = append(det, "gw "+d.Gateway4)
		}
		if len(d.DNS) > 0 {
			det = append(det, "dns "+strings.Join(d.DNS, " "))
		}
		if len(d.IPv6) > 0 {
			det = append(det, "v6 "+joinIPs(d.IPv6))
		}
		if len(det) > 0 {
			lines = append(lines, "", stDim.Render(truncate(strings.Join(det, "  "), w)))
		}
	}
	return strings.Join(lines, "\n")
}
