package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/core"
)

// diagTab is the inspect-only corner: LAN neighbours (with a sweep), listening
// ports, routes, Infra networks (bridge → members), the public IP and a DNS
// lookup box, switched with [ and ].
type diagTab struct {
	sub int

	hosts    []core.LANHost
	hostsErr error
	hostsOK  bool
	sweeping bool

	ports    []core.ListeningPort
	portsErr error
	portsOK  bool

	routes    []core.Route
	routesErr error
	routesOK  bool

	infra    []core.InfraNetwork
	infraErr error
	infraOK  bool

	pub    core.PublicIP
	pubErr error
	pubOK  bool

	dnsInput textinput.Model
	dnsBusy  bool
	dnsAns   *core.DNSAnswer
	dnsErr   error
}

const (
	subLAN = iota
	subPorts
	subRoutes
	subInfra
	subPublicIP
	subDNS
	subCount
)

var subNames = [subCount]string{"LAN", "Ports", "Routes", "Infra", "Public IP", "DNS"}

func (t *diagTab) init() {
	t.dnsInput = newInput("lookup: ", "example.com", 253)
}

func (t *diagTab) capturing() bool { return t.sub == subDNS && t.dnsInput.Focused() }

// load fetches the current sub-view the first time it is shown.
func (t *diagTab) load(m *Model) tea.Cmd {
	switch t.sub {
	case subLAN:
		if !t.hostsOK {
			return m.l.lan(lanDevice(m), false)
		}
	case subPorts:
		if !t.portsOK {
			return m.l.ports()
		}
	case subRoutes:
		if !t.routesOK {
			return m.l.routes()
		}
	case subInfra:
		if !t.infraOK {
			return m.l.infra()
		}
	case subPublicIP:
		if !t.pubOK {
			return m.l.publicIP()
		}
	case subDNS:
		return t.dnsInput.Focus()
	}
	return nil
}

// lanDevice is the device the LAN view scans: the primary connection's, else
// the first connected physical device the daemon reported.
func lanDevice(m *Model) string {
	if p := m.status.Primary; p != nil && len(p.Devices) > 0 {
		return p.Devices[0]
	}
	for _, d := range m.devices.devs {
		if d.Class == core.ClassPhysical && d.State == core.DeviceConnected {
			return d.Name
		}
	}
	return ""
}

func (t *diagTab) reload(m *Model) tea.Cmd {
	switch t.sub {
	case subLAN:
		return m.l.lan(lanDevice(m), false)
	case subPorts:
		return m.l.ports()
	case subRoutes:
		return m.l.routes()
	case subInfra:
		return m.l.infra()
	case subPublicIP:
		return m.l.publicIP()
	}
	return nil
}

func (t *diagTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case lanMsg:
		t.sweeping = false
		t.hostsOK, t.hostsErr = true, msg.err
		if msg.err == nil {
			t.hosts = msg.hosts
		}
	case portsMsg:
		t.portsOK, t.portsErr = true, msg.err
		if msg.err == nil {
			t.ports = msg.ports
		}
	case routesMsg:
		t.routesOK, t.routesErr = true, msg.err
		if msg.err == nil {
			t.routes = msg.routes
		}
	case infraMsg:
		t.infraOK, t.infraErr = true, msg.err
		if msg.err == nil {
			t.infra = msg.nets
		}
	case publicIPMsg:
		t.pubOK, t.pubErr = true, msg.err
		if msg.err == nil {
			t.pub = msg.ip
		}
	case dnsMsg:
		t.dnsBusy = false
		t.dnsErr = msg.err
		if msg.err == nil {
			a := msg.answer
			t.dnsAns = &a
		}
	case tea.KeyMsg:
		return t.key(m, msg)
	}
	return nil
}

func (t *diagTab) key(m *Model, k tea.KeyMsg) tea.Cmd {
	if t.capturing() {
		switch k.String() {
		case "esc":
			t.dnsInput.Blur()
			return nil
		case "[", "]":
			t.dnsInput.Blur()
		case "enter":
			name := strings.TrimSpace(t.dnsInput.Value())
			if name == "" {
				return nil
			}
			t.dnsBusy = true
			return m.l.dns(name)
		default:
			var cmd tea.Cmd
			t.dnsInput, cmd = t.dnsInput.Update(k)
			return cmd
		}
	}
	switch k.String() {
	case "]", "right", "l":
		t.sub = (t.sub + 1) % subCount
		return t.load(m)
	case "[", "left", "h":
		t.sub = (t.sub + subCount - 1) % subCount
		return t.load(m)
	case "r":
		return t.reload(m)
	case "s":
		if t.sub == subLAN && !t.sweeping {
			t.sweeping = true
			return tea.Batch(m.spin.Tick, m.l.lan(lanDevice(m), true))
		}
	case "enter", "/":
		if t.sub == subDNS {
			return t.dnsInput.Focus()
		}
	}
	return nil
}

func (t *diagTab) hints(m *Model) string {
	h := keyHints("[ ]", "sub-view", "r", "reload")
	switch t.sub {
	case subLAN:
		h += "  " + keyHints("s", "sweep subnet")
	case subDNS:
		if t.capturing() {
			return keyHints("enter", "lookup", "esc", "leave box", "[ ]", "sub-view")
		}
		h += "  " + keyHints("enter", "lookup box")
	}
	return h
}

func (t *diagTab) view(m *Model, w, h int) string {
	var tabs []string
	for i, n := range subNames {
		if i == t.sub {
			tabs = append(tabs, stRailOn.Render("["+n+"]"))
		} else {
			tabs = append(tabs, stRailOff.Render(" "+n+" "))
		}
	}
	lines := []string{fit(stTitle.Render("Diag")+"  "+strings.Join(tabs, " "), w)}
	body := ""
	switch t.sub {
	case subLAN:
		body = t.lanView(m, w, h-1)
	case subPorts:
		body = t.portsView(w, h-1)
	case subRoutes:
		body = t.routesView(w, h-1)
	case subInfra:
		body = t.infraView(w, h-1)
	case subPublicIP:
		body = t.publicIPView(w)
	case subDNS:
		body = t.dnsView(w)
	}
	lines = append(lines, body)
	return strings.Join(lines, "\n")
}

func loadingOr(ok bool, err error, empty string, w int) (string, bool) {
	switch {
	case err != nil:
		return stBad.Render(truncate("error: "+errText(err), w)), true
	case !ok:
		return stDim.Render("loading…"), true
	}
	return stDim.Render(empty), false
}

func (t *diagTab) lanView(m *Model, w, h int) string {
	head := ""
	if t.sweeping {
		head = m.spin.View() + " " + stDim.Render("sweeping the subnet…")
	}
	if s, stop := loadingOr(t.hostsOK, t.hostsErr, "no neighbours seen; press s to sweep", w); stop || len(t.hosts) == 0 {
		return head + "\n" + s
	}
	cells := make([][]string, 0, len(t.hosts))
	for _, hst := range t.hosts {
		tag := ""
		switch {
		case hst.Gateway:
			tag = stAccent.Render("gateway")
		case hst.Self:
			tag = stDim.Render("this host")
		}
		cells = append(cells, []string{hst.IP, hst.MAC, hst.Hostname, stateStyle(hst.State).Render(hst.State), ago(hst.Seen, m.now()), tag})
	}
	cells = capRows(cells, h-2)
	return head + "\n" + table(w, []int{15, 17, 0, 9, 8, 9}, []string{"ip", "mac", "hostname", "state", "seen", ""}, cells, -1)
}

func (t *diagTab) portsView(w, h int) string {
	if s, stop := loadingOr(t.portsOK, t.portsErr, "nothing listening", w); stop || len(t.ports) == 0 {
		return s
	}
	cells := make([][]string, 0, len(t.ports))
	for _, p := range t.ports {
		proc := p.Process
		if p.PID > 0 {
			proc = fmt.Sprintf("%s (%d)", p.Process, p.PID)
		}
		cells = append(cells, []string{p.Proto, p.Addr, strconv.Itoa(p.Port), proc, p.User})
	}
	cells = capRows(cells, h-1)
	return table(w, []int{5, 24, 6, 0, 10}, []string{"proto", "addr", "port", "process", "user"}, cells, -1)
}

func (t *diagTab) routesView(w, h int) string {
	if s, stop := loadingOr(t.routesOK, t.routesErr, "no routes", w); stop || len(t.routes) == 0 {
		return s
	}
	cells := make([][]string, 0, len(t.routes))
	for _, r := range t.routes {
		via := r.Gateway
		if via == "" {
			via = stDim.Render("direct")
		}
		extra := r.Proto
		if r.Scope != "" {
			extra += " " + r.Scope
		}
		if r.Table > 0 && r.Table != 254 {
			extra += fmt.Sprintf(" table %d", r.Table)
		}
		cells = append(cells, []string{r.Dest, via, r.Device, strconv.Itoa(r.Metric), extra})
	}
	cells = capRows(cells, h-1)
	return table(w, []int{0, 20, 12, 7, 18}, []string{"dest", "via", "dev", "metric", ""}, cells, -1)
}

func (t *diagTab) infraView(w, h int) string {
	if s, stop := loadingOr(t.infraOK, t.infraErr, "no bridges from Docker, Podman, libvirt, Incus or nspawn", w); stop || len(t.infra) == 0 {
		return s
	}
	var lines []string
	for _, n := range t.infra {
		up := stGood.Render("up")
		if !n.Bridge.Up {
			up = stDim.Render("down")
		}
		owner := n.Owner
		if n.OwnerName != "" {
			owner += "/" + n.OwnerName
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s", stBold.Render(n.Bridge.Name), stAccent.Render(owner), up, stDim.Render(strings.Join(n.Bridge.Addresses, " "))))
		for i, mbr := range n.Members {
			branch := "├─"
			if i == len(n.Members)-1 {
				branch = "└─"
			}
			state := stGood.Render("up")
			if !mbr.Up {
				state = stDim.Render("down")
			}
			ns := ""
			if mbr.PeerNetNS >= 0 {
				ns = stDim.Render(fmt.Sprintf(" netns %d", mbr.PeerNetNS))
			}
			lines = append(lines, fmt.Sprintf("  %s %s %s %s%s", stDim.Render(branch), pad(mbr.Name, 16), stDim.Render(mbr.Kind), state, ns))
		}
		if len(n.Neighbours) > 0 {
			var ips []string
			for _, nb := range n.Neighbours {
				ips = append(ips, nb.IP)
			}
			lines = append(lines, "     "+stDim.Render("hosts: "+strings.Join(ips, " ")))
		}
		if n.Reachable != "" && n.Reachable != "ok" {
			lines = append(lines, "     "+stWarn.Render(n.Source+": "+n.Reachable))
		}
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = truncate(lines[i], w)
	}
	return strings.Join(lines, "\n")
}

func (t *diagTab) publicIPView(w int) string {
	if s, stop := loadingOr(t.pubOK, t.pubErr, "", w); stop {
		return s
	}
	p := t.pub
	lines := []string{stBold.Render("public IP  ") + stAccent.Render(p.IP)}
	if p.Location != "" || p.Colo != "" {
		lines = append(lines, stDim.Render("location   ")+p.Location+" "+p.Colo)
	}
	if p.Warp != "" {
		lines = append(lines, stDim.Render("warp       ")+p.Warp)
	}
	lines = append(lines, stDim.Render("via        ")+p.Via)
	return strings.Join(lines, "\n")
}

func (t *diagTab) dnsView(w int) string {
	lines := []string{t.dnsInput.View()}
	switch {
	case t.dnsBusy:
		lines = append(lines, stDim.Render("resolving…"))
	case t.dnsErr != nil:
		lines = append(lines, stBad.Render(truncate("✗ "+errText(t.dnsErr), w)))
	case t.dnsAns != nil:
		a := t.dnsAns
		lines = append(lines, "")
		meta := a.Type
		if a.Server != "" {
			meta += " via " + a.Server
		}
		lines = append(lines, stBold.Render(a.Name)+stDim.Render("  "+meta+"  "+ms(float64(a.Duration)/1e6)))
		if a.Error != "" {
			lines = append(lines, stBad.Render(a.Error))
		}
		for _, ans := range a.Answers {
			lines = append(lines, "  "+stAccent.Render(ans))
		}
		if len(a.Answers) == 0 && a.Error == "" {
			lines = append(lines, stDim.Render("  no answers"))
		}
	default:
		lines = append(lines, stDim.Render("type a name and press enter"))
	}
	return strings.Join(lines, "\n")
}

func capRows[T any](rows []T, n int) []T {
	if n < 1 {
		n = 1
	}
	if len(rows) > n {
		return rows[:n]
	}
	return rows
}
