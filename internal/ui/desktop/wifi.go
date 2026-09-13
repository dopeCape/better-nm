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

type wifiData struct {
	enabled  bool
	networks []core.WifiNetwork
	profiles map[string]core.Profile // by UUID
	err      error
}

// wifiRow is one list row's widgets.
type wifiRow struct {
	name   *widget.Label
	meta   *widget.Label
	tag    *widget.Label
	signal *signalBar
	pct    *widget.Label
	box    fyne.CanvasObject
}

type wifiView struct {
	a   *App
	gen atomic.Int64

	data       wifiData
	filtered   []core.WifiNetwork
	selected   string // SSID
	lastDetail string // SSID the detail pane was last rendered for

	toolbarErr *errorLabel
	loadErr    *errorLabel
	enabled    *widget.Check
	filter     *widget.Entry
	list       *widget.List
	rows       map[fyne.CanvasObject]*wifiRow
	empty      *widget.Label

	// detail pane
	detail      *fyne.Container
	placeholder fyne.CanvasObject
	title       *widget.Label
	tags        *fyne.Container
	info        *kv
	password    *widget.Entry
	username    *widget.Entry
	connectBtn  *widget.Button
	forgetBtn   *widget.Button
	autoconnect *widget.Check
	actErr      *errorLabel
	ipMethod    *widget.Select
	ipAddrs     *widget.Entry
	ipGateway   *widget.Entry
	ipDNS       *widget.Entry
	ipApply     *widget.Button
	ipErr       *errorLabel
	ipBox       *fyne.Container
}

func newWifiView(a *App) *wifiView { return &wifiView{a: a} }

func (v *wifiView) build() fyne.CanvasObject {
	v.toolbarErr = newErrorLabel()
	v.loadErr = newErrorLabel()
	rescan := widget.NewButtonWithIcon("Rescan", theme.ViewRefreshIcon(), v.rescan)
	v.enabled = widget.NewCheck("Wi-Fi on", func(on bool) {
		v.toolbarErr.set(nil)
		v.a.bg(func(ctx context.Context) {
			if err := v.a.c.SetWifiEnabled(ctx, on); err != nil {
				v.a.onUI(func() { v.toolbarErr.set(err); setCheckedSilently(v.enabled, !on) })
			}
		})
	})
	v.filter = widget.NewEntry()
	v.filter.SetPlaceHolder("Filter networks")
	v.filter.OnChanged = func(string) { v.applyFilter() }
	toolbar := container.NewBorder(nil, nil, container.NewHBox(rescan, v.enabled), nil, fixedWidth(v.filter, 220))

	v.rows = map[fyne.CanvasObject]*wifiRow{}
	v.list = widget.NewList(
		func() int { return len(v.filtered) },
		func() fyne.CanvasObject {
			r := v.newRow()
			v.rows[r.box] = r
			return r.box
		},
		func(id widget.ListItemID, o fyne.CanvasObject) { v.updateRow(id, o) },
	)
	v.list.OnSelected = func(id widget.ListItemID) {
		if id < len(v.filtered) {
			v.selected = v.filtered[id].SSID
			v.renderDetail()
		}
	}
	v.list.OnUnselected = func(widget.ListItemID) {}
	v.empty = caption("No networks in range")
	v.empty.Hide()

	v.buildDetail()
	split := container.NewHSplit(container.NewStack(v.list, container.NewCenter(v.empty)), container.NewVScroll(inset(v.detail, 0, 0, 0, 16)))
	split.Offset = 0.42
	return container.NewBorder(inset(container.NewVBox(toolbar, v.toolbarErr, v.loadErr), 16, 24, 8, 24), nil, nil, nil, inset(split, 0, 24, 16, 24))
}

func (v *wifiView) newRow() *wifiRow {
	r := &wifiRow{name: bold(""), meta: caption(""), tag: caption(""), signal: newSignalBar(), pct: mono("")}
	r.name.Truncation = fyne.TextTruncateEllipsis
	r.tag.Importance = widget.SuccessImportance
	top := container.NewBorder(nil, nil, nil, container.NewHBox(fixedWidth(r.signal, 56), fixedWidth(r.pct, 36)), r.name)
	bottom := container.NewHBox(r.meta, r.tag)
	r.box = inset(container.NewVBox(top, inset(bottom, -8, 0, 0, 0)), 2, 8, 2, 4)
	return r
}

func (v *wifiView) updateRow(id widget.ListItemID, o fyne.CanvasObject) {
	if id >= len(v.filtered) {
		return
	}
	n := v.filtered[id]
	r, ok := v.rows[o]
	if !ok {
		return
	}
	r.name.SetText(n.SSID)
	r.meta.SetText(strings.TrimSpace(bandName(n.Band) + "   " + securityName(n.Security)))
	switch {
	case n.Active:
		r.tag.SetText("connected")
		r.tag.Importance = widget.SuccessImportance
	case n.Known:
		r.tag.SetText("saved")
		r.tag.Importance = widget.LowImportance
	default:
		r.tag.SetText("")
	}
	r.tag.Refresh()
	r.signal.set(n.Strength)
	r.pct.SetText(fmt.Sprintf("%d", n.Strength))
}

func (v *wifiView) buildDetail() {
	v.title = widget.NewLabel("")
	v.title.TextStyle = fyne.TextStyle{Bold: true}
	v.title.SizeName = theme.SizeNameHeadingText
	v.tags = container.NewHBox()
	v.info = newKV("Signal", "Band", "Security", "Access points", "Last seen")
	v.password = widget.NewPasswordEntry()
	v.password.SetPlaceHolder("Password")
	v.username = widget.NewEntry()
	v.username.SetPlaceHolder("Username")
	v.connectBtn = widget.NewButton("Connect", v.connect)
	v.connectBtn.Importance = widget.HighImportance
	v.forgetBtn = widget.NewButton("Forget", v.forget)
	v.autoconnect = widget.NewCheck("Connect automatically", v.setAutoconnect)
	v.actErr = newErrorLabel()

	v.ipMethod = widget.NewSelect([]string{"Automatic (DHCP)", "Manual"}, func(string) { v.ipFieldsEnabled() })
	v.ipAddrs = widget.NewEntry()
	v.ipAddrs.SetPlaceHolder("192.168.1.50/24")
	v.ipGateway = widget.NewEntry()
	v.ipGateway.SetPlaceHolder("192.168.1.1")
	v.ipDNS = widget.NewEntry()
	v.ipDNS.SetPlaceHolder("1.1.1.1, 9.9.9.9")
	v.ipApply = widget.NewButton("Apply IP settings", v.applyIP)
	v.ipErr = newErrorLabel()
	ipForm := widget.NewForm(
		widget.NewFormItem("Method", v.ipMethod),
		widget.NewFormItem("Addresses", v.ipAddrs),
		widget.NewFormItem("Gateway", v.ipGateway),
		widget.NewFormItem("DNS", v.ipDNS),
	)
	v.ipBox = container.NewVBox(section("IPv4", container.NewVBox(ipForm, inset(container.NewHBox(v.ipApply), 8, 0, 0, 0), v.ipErr)))

	v.placeholder = container.NewCenter(caption("Select a network"))
	v.detail = container.NewVBox(v.placeholder)
}

// --- data --------------------------------------------------------------------------

func (v *wifiView) refresh() {
	gen := v.gen.Add(1)
	var d wifiData
	v.a.apply(func(ctx context.Context) error {
		st, err := v.a.c.Status(ctx)
		if err != nil {
			d.err = err
			return err
		}
		d.enabled = st.WifiEnabled
		if d.networks, err = v.a.c.Wifi(ctx, ""); err != nil {
			d.err = err
			return err
		}
		ps, _ := v.a.c.Profiles(ctx)
		d.profiles = map[string]core.Profile{}
		for _, p := range ps {
			d.profiles[p.UUID] = p
		}
		return nil
	}, func() {
		if gen != v.gen.Load() {
			return
		}
		v.render(d)
	})
}

func (v *wifiView) render(d wifiData) {
	v.loadErr.set(d.err)
	if d.err != nil {
		return
	}
	v.data = d
	if v.selected == "" {
		// first load: open the detail pane on the network we are on
		for _, n := range d.networks {
			if n.Active {
				v.selected = n.SSID
			}
		}
	}
	if v.enabled.Checked != d.enabled {
		setCheckedSilently(v.enabled, d.enabled)
	}
	v.applyFilter()
	v.renderDetail()
}

func (v *wifiView) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(v.filter.Text))
	v.filtered = v.filtered[:0]
	for _, n := range v.data.networks {
		if q == "" || strings.Contains(strings.ToLower(n.SSID), q) {
			v.filtered = append(v.filtered, n)
		}
	}
	sort.SliceStable(v.filtered, func(i, j int) bool {
		a, b := v.filtered[i], v.filtered[j]
		if a.Active != b.Active {
			return a.Active
		}
		if a.Known != b.Known {
			return a.Known
		}
		return a.Strength > b.Strength
	})
	if len(v.filtered) == 0 {
		v.empty.Show()
	} else {
		v.empty.Hide()
	}
	v.list.Refresh()
	for i, n := range v.filtered {
		if n.SSID == v.selected {
			v.list.Select(i)
			return
		}
	}
}

func (v *wifiView) current() (core.WifiNetwork, bool) {
	for _, n := range v.data.networks {
		if n.SSID == v.selected {
			return n, true
		}
	}
	return core.WifiNetwork{}, false
}

func (v *wifiView) renderDetail() {
	n, ok := v.current()
	if !ok {
		v.detail.Objects = []fyne.CanvasObject{v.placeholder}
		v.detail.Refresh()
		return
	}
	v.title.SetText(n.SSID)
	v.tags.Objects = nil
	if n.Active {
		v.tags.Add(newBadge("connected"))
	} else if n.Known {
		v.tags.Add(newBadge("saved"))
	}
	if n.Hidden {
		v.tags.Add(newBadge("hidden"))
	}
	v.info.setAll(
		fmt.Sprintf("%d%%", n.Strength),
		strings.TrimSpace(fmt.Sprintf("%s  channel %d  %d MHz", bandName(n.Band), n.Channel, n.Frequency)),
		securityName(n.Security),
		fmt.Sprintf("%d", len(n.BSSIDs)),
		fmtAgo(n.LastSeen),
	)

	needsPassword := !n.Known && n.Security != core.SecOpen && n.Security != core.SecOWE
	needsUser := !n.Known && n.Security == core.SecWPAEAP
	if n.SSID != v.lastDetail {
		// a new selection: drop what was typed for the previous one; a mere
		// rescan must not wipe a password mid-typing
		v.lastDetail = n.SSID
		v.password.SetText("")
		v.username.SetText("")
		v.actErr.set(nil)
	}
	if n.Active {
		v.connectBtn.SetText("Disconnect")
		v.connectBtn.Importance = widget.MediumImportance
	} else {
		v.connectBtn.SetText("Connect")
		v.connectBtn.Importance = widget.HighImportance
	}
	v.connectBtn.Refresh()

	actions := container.NewVBox()
	if needsUser {
		actions.Add(v.username)
	}
	if needsPassword {
		actions.Add(v.password)
	}
	row := container.NewHBox(v.connectBtn)
	if n.Known {
		row.Add(v.forgetBtn)
	}
	actions.Add(inset(row, 4, 0, 0, 0))
	actions.Add(v.actErr)

	objs := []fyne.CanvasObject{
		container.NewHBox(v.title, inset(v.tags, 6, 0, 0, 8)),
		inset(v.info.box, 4, 0, 16, 0),
		actions,
	}
	if n.Known {
		if p, ok := v.data.profiles[n.ProfileUUID]; ok {
			setCheckedSilently(v.autoconnect, p.Autoconnect)
			objs = append(objs, inset(v.autoconnect, 8, 0, 8, 0))
			v.fillIP(p.IPv4)
			objs = append(objs, v.ipBox)
		}
	}
	v.detail.Objects = objs
	v.detail.Refresh()
}

func (v *wifiView) fillIP(ip core.IPConfig) {
	v.ipErr.set(nil)
	if ip.Method == core.IPManual {
		v.ipMethod.SetSelected("Manual")
	} else {
		v.ipMethod.SetSelected("Automatic (DHCP)")
	}
	v.ipAddrs.SetText(strings.Join(ip.Addresses, ", "))
	v.ipGateway.SetText(ip.Gateway)
	v.ipDNS.SetText(strings.Join(ip.DNS, ", "))
	v.ipFieldsEnabled()
}

func (v *wifiView) ipFieldsEnabled() {
	manual := v.ipMethod.Selected == "Manual"
	for _, e := range []*widget.Entry{v.ipAddrs, v.ipGateway} {
		if manual {
			e.Enable()
		} else {
			e.Disable()
		}
	}
}

// --- actions -----------------------------------------------------------------------

func (v *wifiView) rescan() {
	v.toolbarErr.set(nil)
	v.a.bg(func(ctx context.Context) {
		if err := v.a.c.ScanWifi(ctx, ""); err != nil {
			v.a.onUI(func() { v.toolbarErr.set(err) })
		}
	})
}

func (v *wifiView) connect() {
	n, ok := v.current()
	if !ok {
		return
	}
	v.actErr.set(nil)
	v.connectBtn.Disable()
	if n.Active {
		v.a.bg(func(ctx context.Context) {
			err := v.a.c.DisconnectWifi(ctx, n.Device)
			v.a.onUI(func() { v.connectBtn.Enable(); v.actErr.set(err) })
		})
		return
	}
	req := core.ConnectWifiRequest{Device: n.Device, SSID: n.SSID, Hidden: n.Hidden}
	if !n.Known {
		req.Password = v.password.Text
		req.Username = v.username.Text
	}
	v.a.bg(func(ctx context.Context) {
		var err error
		if n.Known {
			err = v.a.c.ActivateProfile(ctx, n.ProfileUUID, n.Device)
		} else {
			err = v.a.c.ConnectWifi(ctx, req)
		}
		v.a.onUI(func() {
			v.connectBtn.Enable()
			v.actErr.set(err)
			if err == nil {
				v.password.SetText("")
			}
		})
	})
}

func (v *wifiView) forget() {
	n, ok := v.current()
	if !ok || n.ProfileUUID == "" {
		return
	}
	v.actErr.set(nil)
	uuid := n.ProfileUUID
	v.a.bg(func(ctx context.Context) {
		if err := v.a.c.ForgetWifi(ctx, uuid); err != nil {
			v.a.onUI(func() { v.actErr.set(err) })
		}
	})
}

func (v *wifiView) setAutoconnect(on bool) {
	n, ok := v.current()
	if !ok || n.ProfileUUID == "" {
		return
	}
	uuid := n.ProfileUUID
	v.a.bg(func(ctx context.Context) {
		if err := v.a.c.SetAutoconnect(ctx, uuid, on); err != nil {
			v.a.onUI(func() { v.actErr.set(err) })
		}
	})
}

func (v *wifiView) applyIP() {
	n, ok := v.current()
	if !ok || n.ProfileUUID == "" {
		return
	}
	cfg := core.IPConfig{Method: core.IPAuto}
	if v.ipMethod.Selected == "Manual" {
		cfg.Method = core.IPManual
		cfg.Addresses = splitList(v.ipAddrs.Text)
		cfg.Gateway = strings.TrimSpace(v.ipGateway.Text)
	}
	cfg.DNS = splitList(v.ipDNS.Text)
	if cfg.Method == core.IPManual && len(cfg.Addresses) == 0 {
		v.ipErr.set(fmt.Errorf("manual addressing needs at least one address in CIDR form, e.g. 192.168.1.50/24"))
		return
	}
	v.ipErr.set(nil)
	uuid := n.ProfileUUID
	v.a.bg(func(ctx context.Context) {
		err := v.a.c.SetProfileIP(ctx, uuid, &cfg, nil)
		v.a.onUI(func() { v.ipErr.set(err) })
	})
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
