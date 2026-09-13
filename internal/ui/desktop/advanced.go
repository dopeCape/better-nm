package desktop

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/core"
)

// stringTable is a read-only table over rows of strings with a header.
type stringTable struct {
	table   *widget.Table
	columns []string
	rows    [][]string
}

func newStringTable(columns []string, widths []float32) *stringTable {
	t := &stringTable{columns: columns}
	t.table = widget.NewTable(
		func() (int, int) { return len(t.rows), len(t.columns) },
		func() fyne.CanvasObject {
			l := mono("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			if id.Row < len(t.rows) && id.Col < len(t.rows[id.Row]) {
				o.(*widget.Label).SetText(t.rows[id.Row][id.Col])
			}
		},
	)
	t.table.ShowHeaderRow = true
	t.table.CreateHeader = func() fyne.CanvasObject { return caption("") }
	t.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		if id.Col >= 0 && id.Col < len(t.columns) {
			o.(*widget.Label).SetText(t.columns[id.Col])
		}
	}
	for i, w := range widths {
		t.table.SetColumnWidth(i, w)
	}
	return t
}

func (t *stringTable) set(rows [][]string) {
	t.rows = rows
	t.table.Refresh()
}

type advancedView struct {
	a   *App
	gen atomic.Int64

	lan      *stringTable
	lanErr   *errorLabel
	lanEmpty *widget.Label
	ports    *stringTable
	portsErr *errorLabel
	routes   *stringTable
	rtErr    *errorLabel

	infraTree  *widget.Tree
	infraNodes map[string][]string
	infraLabel map[string]string
	infraErr   *errorLabel

	dnsName   *widget.Entry
	dnsServer *widget.Entry
	dnsType   *widget.Select
	dnsOut    *kv
	dnsErr    *errorLabel

	pubIP  *kv
	pubErr *errorLabel
}

func newAdvancedView(a *App) *advancedView {
	return &advancedView{a: a, infraNodes: map[string][]string{}, infraLabel: map[string]string{}}
}

func (v *advancedView) build() fyne.CanvasObject {
	// LAN
	v.lan = newStringTable([]string{"IP", "Hostname", "MAC", "State", "Device"}, []float32{140, 200, 150, 90, 80})
	v.lanErr = newErrorLabel()
	v.lanEmpty = caption("Nothing in the neighbour table. Sweep pings the subnet to populate it.")
	sweep := widget.NewButtonWithIcon("Sweep subnet", theme.SearchIcon(), func() { v.loadLAN(true) })
	lanTab := container.NewBorder(inset(container.NewVBox(container.NewHBox(sweep), v.lanErr, v.lanEmpty), 12, 0, 8, 0), nil, nil, nil, v.lan.table)

	// Ports
	v.ports = newStringTable([]string{"Proto", "Address", "Port", "Process", "User"}, []float32{60, 200, 70, 200, 100})
	v.portsErr = newErrorLabel()
	portsTab := container.NewBorder(inset(v.portsErr, 12, 0, 0, 0), nil, nil, nil, v.ports.table)

	// Routes
	v.routes = newStringTable([]string{"Destination", "Gateway", "Device", "Metric", "Proto", "Table"}, []float32{200, 150, 100, 70, 80, 60})
	v.rtErr = newErrorLabel()
	routesTab := container.NewBorder(inset(v.rtErr, 12, 0, 0, 0), nil, nil, nil, v.routes.table)

	// Infra
	v.infraErr = newErrorLabel()
	v.infraTree = widget.NewTree(
		func(id widget.TreeNodeID) []widget.TreeNodeID { return v.infraNodes[id] },
		func(id widget.TreeNodeID) bool { return len(v.infraNodes[id]) > 0 },
		func(bool) fyne.CanvasObject { return mono("") },
		func(id widget.TreeNodeID, _ bool, o fyne.CanvasObject) { o.(*widget.Label).SetText(v.infraLabel[id]) },
	)
	infraTab := container.NewBorder(inset(v.infraErr, 12, 0, 0, 0), nil, nil, nil, v.infraTree)

	// DNS
	v.dnsName = widget.NewEntry()
	v.dnsName.SetPlaceHolder("example.com")
	v.dnsServer = widget.NewEntry()
	v.dnsServer.SetPlaceHolder("system resolver")
	v.dnsType = widget.NewSelect([]string{"A", "AAAA", "MX", "TXT", "CNAME", "NS"}, nil)
	v.dnsType.SetSelected("A")
	v.dnsOut = newKV("Answers", "Server", "Time")
	v.dnsErr = newErrorLabel()
	lookup := widget.NewButton("Look up", v.lookup)
	lookup.Importance = widget.HighImportance
	v.dnsName.OnSubmitted = func(string) { v.lookup() }
	dnsForm := widget.NewForm(
		widget.NewFormItem("Name", v.dnsName),
		widget.NewFormItem("Server", v.dnsServer),
		widget.NewFormItem("Type", v.dnsType),
	)
	dnsTab := inset(container.NewVBox(dnsForm, inset(container.NewHBox(lookup), 8, 0, 16, 0), v.dnsOut.box, v.dnsErr), 12, 0, 0, 0)

	// Public IP
	v.pubIP = newKV("IP", "Location", "Via")
	v.pubErr = newErrorLabel()
	pubBtn := widget.NewButtonWithIcon("Check public IP", theme.ViewRefreshIcon(), v.loadPublicIP)
	pubTab := inset(container.NewVBox(container.NewHBox(pubBtn), inset(v.pubIP.box, 12, 0, 0, 0), v.pubErr), 12, 0, 0, 0)

	tabs := container.NewAppTabs(
		container.NewTabItem("LAN", lanTab),
		container.NewTabItem("Ports", portsTab),
		container.NewTabItem("Routes", routesTab),
		container.NewTabItem("Infra", infraTab),
		container.NewTabItem("DNS", dnsTab),
		container.NewTabItem("Public IP", pubTab),
	)
	return inset(tabs, 12, 24, 24, 24)
}

func (v *advancedView) refresh() {
	v.gen.Add(1)
	v.loadLAN(false)
	v.loadPorts()
	v.loadRoutes()
	v.loadInfra()
}

func (v *advancedView) loadLAN(sweep bool) {
	var hosts []core.LANHost
	var err error
	v.a.apply(func(ctx context.Context) error {
		// the daemon needs a device name; use the primary connection's
		device := ""
		if st, serr := v.a.c.Status(ctx); serr == nil && st.Primary != nil && len(st.Primary.Devices) > 0 {
			device = st.Primary.Devices[0]
		}
		hosts, err = v.a.c.LANHosts(ctx, device, sweep)
		return err
	}, func() {
		v.lanErr.set(err)
		rows := make([][]string, 0, len(hosts))
		for _, h := range hosts {
			name := h.Hostname
			if h.Self {
				name = strings.TrimSpace(name + " (this machine)")
			} else if h.Gateway {
				name = strings.TrimSpace(name + " (gateway)")
			}
			rows = append(rows, []string{h.IP, name, h.MAC, h.State, h.Device})
		}
		if len(rows) == 0 {
			if err == nil {
				v.lanEmpty.Show()
			}
			v.lan.table.Hide()
		} else {
			v.lanEmpty.Hide()
			v.lan.table.Show()
		}
		v.lan.set(rows)
	})
}

func (v *advancedView) loadPorts() {
	var ports []core.ListeningPort
	var err error
	v.a.apply(func(ctx context.Context) error {
		ports, err = v.a.c.ListeningPorts(ctx)
		return err
	}, func() {
		v.portsErr.set(err)
		sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
		rows := make([][]string, 0, len(ports))
		for _, p := range ports {
			proc := p.Process
			if p.PID > 0 {
				proc = fmt.Sprintf("%s (%d)", p.Process, p.PID)
			}
			rows = append(rows, []string{p.Proto, p.Addr, fmt.Sprintf("%d", p.Port), proc, p.User})
		}
		v.ports.set(rows)
	})
}

func (v *advancedView) loadRoutes() {
	var routes []core.Route
	var err error
	v.a.apply(func(ctx context.Context) error {
		routes, err = v.a.c.Routes(ctx)
		return err
	}, func() {
		v.rtErr.set(err)
		rows := make([][]string, 0, len(routes))
		for _, r := range routes {
			table := ""
			if r.Table > 0 {
				table = fmt.Sprintf("%d", r.Table)
			}
			rows = append(rows, []string{r.Dest, r.Gateway, r.Device, fmt.Sprintf("%d", r.Metric), r.Proto, table})
		}
		v.routes.set(rows)
	})
}

func (v *advancedView) loadInfra() {
	var nets []core.InfraNetwork
	var err error
	v.a.apply(func(ctx context.Context) error {
		nets, err = v.a.c.Infra(ctx)
		return err
	}, func() {
		v.infraErr.set(err)
		nodes := map[string][]string{}
		labels := map[string]string{}
		for _, n := range nets {
			id := "br:" + n.Bridge.Name
			owner := n.Owner
			if n.OwnerName != "" {
				owner += " " + n.OwnerName
			}
			labels[id] = fmt.Sprintf("%s  %s  %s", n.Bridge.Name, owner, strings.Join(n.Bridge.Addresses, " "))
			nodes[""] = append(nodes[""], id)
			for _, m := range n.Members {
				mid := id + "/" + m.Name
				state := "down"
				if m.Up {
					state = "up"
				}
				labels[mid] = fmt.Sprintf("%s  %s  %s", m.Name, m.Kind, state)
				nodes[id] = append(nodes[id], mid)
			}
			for _, nb := range n.Neighbours {
				nid := id + "/nb/" + nb.IP
				labels[nid] = fmt.Sprintf("%s  %s  %s", nb.IP, nb.MAC, nb.Hostname)
				nodes[id] = append(nodes[id], nid)
			}
		}
		if len(nets) == 0 && err == nil {
			labels["none"] = "No container or VM bridges found"
			nodes[""] = []string{"none"}
		}
		v.infraNodes = nodes
		v.infraLabel = labels
		v.infraTree.Refresh()
		for _, id := range nodes[""] {
			v.infraTree.OpenBranch(id)
		}
	})
}

func (v *advancedView) lookup() {
	name := strings.TrimSpace(v.dnsName.Text)
	if name == "" {
		v.dnsErr.set(fmt.Errorf("enter a name to look up"))
		return
	}
	server := strings.TrimSpace(v.dnsServer.Text)
	qtype := v.dnsType.Selected
	v.dnsErr.set(nil)
	v.dnsOut.setAll("looking up", "", "")
	v.a.bg(func(ctx context.Context) {
		ans, err := v.a.c.DNSLookup(ctx, name, server, qtype)
		v.a.onUI(func() {
			if err != nil {
				v.dnsErr.set(err)
				v.dnsOut.setAll("", "", "")
				return
			}
			if ans.Error != "" {
				v.dnsErr.set(fmt.Errorf("%s", ans.Error))
			}
			v.dnsOut.setAll(joinOr(ans.Answers, "no answer"), ans.Server, fmtMs(float64(ans.Duration.Microseconds())/1000))
		})
	})
}

func (v *advancedView) loadPublicIP() {
	v.pubErr.set(nil)
	v.pubIP.setAll("checking", "", "")
	v.a.bg(func(ctx context.Context) {
		ip, err := v.a.c.PublicIP(ctx)
		v.a.onUI(func() {
			if err != nil {
				v.pubErr.set(err)
				v.pubIP.setAll("", "", "")
				return
			}
			loc := ip.Location
			if ip.Colo != "" {
				loc = strings.TrimSpace(loc + " " + ip.Colo)
			}
			v.pubIP.setAll(ip.IP, loc, ip.Via)
		})
	})
}
