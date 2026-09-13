package desktop

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/core"
)

type devicesData struct {
	devices  []core.Device
	profiles []core.Profile
	err      error
}

type devicesView struct {
	a   *App
	gen atomic.Int64

	loadErr  *errorLabel
	actErr   *errorLabel
	physical *fyne.Container
	virtual  *fyne.Container
	virtBox  *widget.Accordion
	virtItem *widget.AccordionItem
	empty    *widget.Label
}

func newDevicesView(a *App) *devicesView { return &devicesView{a: a} }

func (v *devicesView) build() fyne.CanvasObject {
	v.loadErr = newErrorLabel()
	v.actErr = newErrorLabel()
	v.physical = container.NewVBox()
	v.virtual = container.NewVBox()
	v.empty = caption("No devices")
	v.virtItem = widget.NewAccordionItem("Virtual (0)", v.virtual)
	v.virtBox = widget.NewAccordion(v.virtItem)
	return page(container.NewVBox(
		v.loadErr,
		section("Devices", container.NewVBox(v.empty, v.physical, v.actErr)),
		v.virtBox,
	))
}

func (v *devicesView) refresh() {
	gen := v.gen.Add(1)
	var d devicesData
	v.a.apply(func(ctx context.Context) error {
		var err error
		if d.devices, err = v.a.c.Devices(ctx); err != nil {
			d.err = err
			return err
		}
		d.profiles, _ = v.a.c.Profiles(ctx)
		return nil
	}, func() {
		if gen != v.gen.Load() {
			return
		}
		v.render(d)
	})
}

func (v *devicesView) render(d devicesData) {
	v.loadErr.set(d.err)
	if d.err != nil {
		return
	}
	v.physical.Objects = nil
	v.virtual.Objects = nil
	nPhys, nVirt := 0, 0
	for _, dev := range d.devices {
		switch dev.Class {
		case core.ClassLoopback:
			continue
		case core.ClassPhysical:
			nPhys++
			v.physical.Add(v.row(dev, d.profiles, true))
		default:
			nVirt++
			v.virtual.Add(v.row(dev, d.profiles, false))
		}
	}
	if nPhys == 0 {
		v.empty.Show()
	} else {
		v.empty.Hide()
	}
	v.virtItem.Title = fmt.Sprintf("Virtual (%d)", nVirt)
	if nVirt == 0 {
		v.virtual.Add(caption("No bridges, veths or tunnels"))
	}
	v.physical.Refresh()
	v.virtual.Refresh()
	v.virtBox.Refresh()
}

func (v *devicesView) row(dev core.Device, profiles []core.Profile, physical bool) fyne.CanvasObject {
	st := newDot(8)
	st.setColor(semanticColor(string(dev.State)))
	name := bold(dev.Name)
	kind := newBadge(string(dev.Kind))
	left := container.NewHBox(inset(st, 0, 6, 0, 0), name, inset(kind, 0, 0, 0, 8))
	if dev.Owner != "" {
		left.Add(inset(newBadge(dev.Owner), 0, 0, 0, 4))
	}
	if dev.ActiveName != "" {
		left.Add(inset(caption(dev.ActiveName), 0, 0, 0, 8))
	}
	state := caption(string(dev.State))
	if dev.Speed > 0 && dev.State == core.DeviceConnected {
		state.SetText(fmt.Sprintf("%s  %d Mbit/s", dev.State, dev.Speed))
	}
	addrs := append([]string(nil), dev.IPv4...)
	for _, a := range dev.IPv6 {
		if !strings.HasPrefix(strings.ToLower(a), "fe80:") { // link-local is noise here
			addrs = append(addrs, a)
		}
	}
	ip := mono(strings.Join(addrs, "  "))
	right := container.NewHBox(ip, inset(state, 0, 0, 0, 12))
	if physical && dev.Kind == core.DeviceEthernet && dev.Managed {
		right.Add(inset(v.wiredButton(dev, profiles), 0, 0, 0, 12))
	}
	row := container.NewBorder(nil, nil, left, right)
	return container.NewVBox(inset(row, 2, 0, 2, 0), widget.NewSeparator())
}

// wiredButton is the up/down toggle for an ethernet port.
func (v *devicesView) wiredButton(dev core.Device, profiles []core.Profile) *widget.Button {
	if dev.State == core.DeviceConnected || dev.State == core.DeviceConnecting {
		return widget.NewButton("Down", func() {
			v.actErr.set(nil)
			v.a.bg(func(ctx context.Context) {
				if err := v.a.c.DisconnectWifi(ctx, dev.Name); err != nil {
					v.a.onUI(func() { v.actErr.set(err) })
				}
			})
		})
	}
	// pick the profile bound to this port, else the first wired profile
	var uuid string
	for _, p := range profiles {
		if p.Type == core.ProfileEthernet && (p.InterfaceName == dev.Name || uuid == "") {
			uuid = p.UUID
			if p.InterfaceName == dev.Name {
				break
			}
		}
	}
	b := widget.NewButton("Up", func() {
		v.actErr.set(nil)
		v.a.bg(func(ctx context.Context) {
			if err := v.a.c.ActivateProfile(ctx, uuid, dev.Name); err != nil {
				v.a.onUI(func() { v.actErr.set(err) })
			}
		})
	})
	if uuid == "" || dev.State == core.DeviceUnavailable {
		b.Disable()
	}
	return b
}
