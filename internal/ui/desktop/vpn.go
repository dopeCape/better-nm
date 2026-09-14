package desktop

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/core"
)

type vpnView struct {
	a   *App
	gen atomic.Int64

	vpns    []core.VPN
	rows    *fyne.Container
	empty   *widget.Label
	loadErr *errorLabel
	actErr  *errorLabel
	// switches and state labels by VPN id so tests can find them
	switches map[string]*widget.Check
	states   map[string]*widget.Label

	// tailscale
	tsBox      *fyne.Container
	tsInfo     *kv
	tsExit     *widget.Select
	tsPeerIDs  []string
	tsAllowLAN *widget.Check
	tsDNS      *widget.Check
	tsLogin    *widget.Button
	tsLogout   *widget.Button
	tsHealth   *widget.Label
	tsErr      *errorLabel
	tsGuard    bool // true while programmatically setting widgets
}

func newVPNView(a *App) *vpnView {
	return &vpnView{a: a, switches: map[string]*widget.Check{}, states: map[string]*widget.Label{}}
}

func (v *vpnView) build() fyne.CanvasObject {
	v.rows = container.NewVBox()
	v.empty = caption("No VPNs yet. Add a WireGuard .conf or OpenVPN .ovpn file, or install Tailscale.")
	v.loadErr = newErrorLabel()
	v.actErr = newErrorLabel()
	add := widget.NewButtonWithIcon("Add from file", theme.ContentAddIcon(), v.addFromFile)

	v.tsInfo = newKV("Backend", "Tailnet", "This node", "Addresses", "Version")
	v.tsExit = widget.NewSelect([]string{"None"}, func(string) { v.setExitNode() })
	v.tsAllowLAN = widget.NewCheck("Allow LAN access while using an exit node", func(bool) { v.setExitNode() })
	v.tsDNS = widget.NewCheck("Accept DNS from the tailnet (MagicDNS)", func(on bool) {
		if v.tsGuard {
			return
		}
		v.a.bg(func(ctx context.Context) {
			if err := v.a.c.SetTailscaleAcceptDNS(ctx, on); err != nil {
				v.a.onUI(func() { v.tsErr.set(err) })
			}
		})
	})
	v.tsLogin = widget.NewButton("Log in", v.login)
	v.tsLogout = widget.NewButton("Log out", v.logout)
	v.tsHealth = widget.NewLabel("")
	v.tsHealth.Importance = widget.WarningImportance
	v.tsHealth.Wrapping = fyne.TextWrapWord
	v.tsErr = newErrorLabel()
	exitRow := container.NewHBox(widget.NewLabel("Exit node"), fixedWidth(v.tsExit, 320))
	v.tsBox = container.NewVBox(section("Tailscale", container.NewVBox(
		v.tsInfo.box,
		inset(exitRow, 12, 0, 0, 0),
		v.tsAllowLAN,
		v.tsDNS,
		inset(container.NewHBox(v.tsLogin, v.tsLogout), 8, 0, 0, 0),
		v.tsHealth,
		v.tsErr,
	)))
	v.tsBox.Hide()

	return page(container.NewVBox(
		v.loadErr,
		section("VPNs", container.NewVBox(v.empty, v.rows, v.actErr, inset(container.NewHBox(add), 12, 0, 0, 0))),
		v.tsBox,
	))
}

func (v *vpnView) refresh() {
	gen := v.gen.Add(1)
	var vpns []core.VPN
	var loadErr error
	v.a.apply(func(ctx context.Context) error {
		vpns, loadErr = v.a.c.VPNs(ctx)
		return loadErr
	}, func() {
		if gen != v.gen.Load() {
			return
		}
		v.loadErr.set(loadErr)
		if loadErr != nil {
			return
		}
		v.render(vpns)
	})
}

func (v *vpnView) render(vpns []core.VPN) {
	v.vpns = vpns
	v.rows.Objects = nil
	v.switches = map[string]*widget.Check{}
	v.states = map[string]*widget.Label{}
	var ts *core.VPN
	for i := range vpns {
		vpn := vpns[i]
		v.rows.Add(v.row(vpn))
		if vpn.Tailscale != nil {
			ts = &vpns[i]
		}
	}
	if len(vpns) == 0 {
		v.empty.Show()
	} else {
		v.empty.Hide()
	}
	v.rows.Refresh()
	if ts == nil {
		v.tsBox.Hide()
		return
	}
	v.renderTailscale(*ts)
}

func (v *vpnView) row(vpn core.VPN) fyne.CanvasObject {
	id := vpn.ID
	on := vpn.State == core.VPNConnected || vpn.State == core.VPNConnecting
	check := widget.NewCheck("", nil)
	check.SetChecked(on)
	if !vpn.Writable || vpn.State == core.VPNUnavailable {
		check.Disable()
	}
	check.OnChanged = func(want bool) {
		v.actErr.set(nil)
		v.a.bg(func(ctx context.Context) {
			var err error
			if want {
				err = v.a.c.ConnectVPN(ctx, id)
			} else {
				err = v.a.c.DisconnectVPN(ctx, id)
			}
			if err != nil {
				v.a.onUI(func() { v.actErr.set(err); setCheckedSilently(check, !want) })
			}
		})
	}
	v.switches[id] = check
	st := newDot(8)
	st.setColor(semanticColor(string(vpn.State)))
	state := caption(vpnStateText(vpn))
	v.states[id] = state
	detail := ""
	switch {
	case vpn.WireGuard != nil:
		if len(vpn.WireGuard.Peers) > 0 {
			detail = vpn.WireGuard.Peers[0].Endpoint
		}
	case vpn.NMVPN != nil:
		detail = vpn.NMVPN.Gateway
	case vpn.Tailscale != nil:
		detail = vpn.Tailscale.Tailnet
	}
	name := bold(vpn.Name)
	left := container.NewHBox(check, inset(st, 0, 6, 0, 0), name, inset(kindBadge(vpn), 0, 0, 0, 8))
	if detail != "" {
		left.Add(inset(mono(detail), 0, 0, 0, 8))
	}
	row := container.NewBorder(nil, nil, left, state)
	return container.NewVBox(inset(row, 2, 0, 2, 0), widget.NewSeparator())
}

func (v *vpnView) renderTailscale(vpn core.VPN) {
	ts := vpn.Tailscale
	v.tsGuard = true
	defer func() { v.tsGuard = false }()
	v.tsInfo.setAll(ts.BackendState, ts.Tailnet, ts.SelfName, joinOr(ts.SelfIPs, ""), ts.Version)
	opts := []string{"None"}
	v.tsPeerIDs = []string{""}
	selected := "None"
	for _, p := range ts.Peers {
		if !p.ExitNodeOption && p.ID != ts.ExitNodeID {
			continue
		}
		label := p.Name
		if !p.Online {
			label += " (offline)"
		}
		opts = append(opts, label)
		v.tsPeerIDs = append(v.tsPeerIDs, p.ID)
		if ts.ExitNodeOn && p.ID == ts.ExitNodeID {
			selected = label
		}
	}
	v.tsExit.Options = opts
	v.tsExit.SetSelected(selected)
	v.tsAllowLAN.SetChecked(ts.AllowLAN)
	v.tsDNS.SetChecked(ts.AcceptDNS)
	if vpn.State == core.VPNNeedsAuth || ts.BackendState == "NeedsLogin" {
		v.tsLogin.Importance = widget.HighImportance
	} else {
		v.tsLogin.Importance = widget.MediumImportance
	}
	v.tsLogin.Refresh()
	if !ts.OperatorOK {
		v.tsErr.set(core.Errorf(core.KindPermission, "run: sudo tailscale set --operator=$USER", "bnm cannot drive tailscaled for this user"))
	} else {
		v.tsErr.set(nil)
	}
	if len(ts.Health) > 0 {
		v.tsHealth.SetText(strings.Join(ts.Health, "\n"))
		v.tsHealth.Show()
	} else {
		v.tsHealth.Hide()
	}
	v.tsBox.Show()
	v.tsBox.Refresh()
}

func (v *vpnView) setExitNode() {
	if v.tsGuard {
		return
	}
	idx := -1
	for i, o := range v.tsExit.Options {
		if o == v.tsExit.Selected {
			idx = i
		}
	}
	if idx < 0 || idx >= len(v.tsPeerIDs) {
		return
	}
	peer := v.tsPeerIDs[idx]
	allowLAN := v.tsAllowLAN.Checked
	v.tsErr.set(nil)
	v.a.bg(func(ctx context.Context) {
		if err := v.a.c.SetTailscaleExitNode(ctx, peer, allowLAN); err != nil {
			v.a.onUI(func() { v.tsErr.set(err) })
		}
	})
}

func (v *vpnView) login() {
	v.tsErr.set(nil)
	v.a.bg(func(ctx context.Context) {
		u, err := v.a.c.TailscaleLogin(ctx)
		if err != nil {
			v.a.onUI(func() { v.tsErr.set(err) })
			return
		}
		parsed, perr := url.Parse(u)
		if perr != nil {
			v.a.onUI(func() { v.tsErr.set(fmt.Errorf("login URL %q: %w", u, perr)) })
			return
		}
		v.a.onUI(func() {
			if err := v.a.fy.OpenURL(parsed); err != nil {
				v.tsErr.set(fmt.Errorf("open %s in your browser to finish logging in", u))
			}
		})
	})
}

func (v *vpnView) logout() {
	v.tsErr.set(nil)
	v.a.bg(func(ctx context.Context) {
		if err := v.a.c.TailscaleLogout(ctx); err != nil {
			v.a.onUI(func() { v.tsErr.set(err) })
		}
	})
}

// addFromFile opens a file picker for a WireGuard .conf or OpenVPN .ovpn and
// imports its content through the daemon.
func (v *vpnView) addFromFile() {
	fd := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			v.actErr.set(err)
			return
		}
		if rc == nil {
			return
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		if err != nil {
			v.actErr.set(err)
			return
		}
		name := rc.URI().Name()
		v.importContent(name, string(data))
	}, v.a.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".conf", ".ovpn"}))
	// Show before Resize: Fyne 2.8's FileDialog builds its inner dialog on Show
	// and Resize dereferences it (nil pointer panic the other way round).
	fd.Show()
	fd.Resize(fyne.NewSize(720, 520))
}

// importContent sends a VPN file's content to the daemon; the kind follows the
// extension.
func (v *vpnView) importContent(filename, content string) {
	req := api.ImportVPNRequest{Content: content}
	ext := strings.ToLower(filepath.Ext(filename))
	req.Name = strings.TrimSuffix(filename, filepath.Ext(filename))
	switch ext {
	case ".conf":
		req.Kind = "wireguard"
	case ".ovpn":
		req.Kind = "openvpn"
	default:
		v.actErr.set(fmt.Errorf("%s: expected a WireGuard .conf or an OpenVPN .ovpn file", filename))
		return
	}
	v.actErr.set(nil)
	v.a.bg(func(ctx context.Context) {
		if _, err := v.a.c.ImportVPN(ctx, req); err != nil {
			v.a.onUI(func() { v.actErr.set(err) })
		}
	})
}

// kindBadge labels the backend kind, except when the kind is the name itself
// ("Tailscale Tailscale" says nothing twice); then it is an empty spacer.
func kindBadge(v core.VPN) fyne.CanvasObject {
	if strings.EqualFold(v.Kind, v.Name) {
		return container.NewWithoutLayout()
	}
	return newBadge(v.Kind)
}
