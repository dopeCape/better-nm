package desktop

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"github.com/godbus/dbus/v5"

	"github.com/dopeCape/better-nm/internal/core"
)

// hasTrayHost reports whether a StatusNotifierWatcher (the tray side of the
// SNI protocol: waybar, swaybar, KDE, GNOME with the AppIndicator extension)
// owns its name on the session bus. Without one the app is window-only.
func hasTrayHost() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var has bool
	call := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher")
	if call.Err != nil || call.Store(&has) != nil {
		return false
	}
	return has
}

// trayState is what the tray menu reflects.
type trayState struct {
	connection string
	wifiOn     bool
	wifiHW     bool
	vpns       []core.VPN
	verdict    string
	secret     string // "Password needed for …" while a request is pending
}

// tray owns the StatusNotifierItem menu; every change rebuilds it.
type tray struct {
	a    *App
	desk desktop.App
	mu   sync.Mutex
	st   trayState
}

func newTray(a *App, desk desktop.App) *tray {
	t := &tray{a: a, desk: desk, st: trayState{connection: "Loading", verdict: "unknown"}}
	if icon := appIcon(); icon != nil {
		desk.SetSystemTrayIcon(icon)
	}
	desk.SetSystemTrayWindow(a.win)
	return t
}

// setStatus is fed by the header refresh (UI thread).
func (t *tray) setStatus(hs headerState) {
	t.mu.Lock()
	t.st.connection = hs.connectionName()
	if hs.status != nil {
		t.st.wifiOn = hs.status.WifiEnabled
		t.st.wifiHW = hs.status.WifiHardware
	}
	t.st.verdict = hs.verdict()
	st := t.st
	t.mu.Unlock()
	t.desk.SetSystemTrayMenu(t.menu(st))
}

// setSecret sets (or with "" clears) the pending-password entry (UI thread).
func (t *tray) setSecret(label string) {
	t.mu.Lock()
	t.st.secret = label
	st := t.st
	t.mu.Unlock()
	t.desk.SetSystemTrayMenu(t.menu(st))
}

// refresh reloads the VPN list off-thread and rebuilds the menu.
func (t *tray) refresh() {
	t.a.bg(func(ctx context.Context) {
		vpns, err := t.a.c.VPNs(ctx)
		if err != nil {
			return
		}
		t.mu.Lock()
		t.st.vpns = vpns
		st := t.st
		t.mu.Unlock()
		t.a.onUI(func() { t.desk.SetSystemTrayMenu(t.menu(st)) })
	})
}

// menu builds the tray menu: connection name, Wi-Fi toggle, one checkable
// item per VPN, the quality verdict, Open, Quit.
func (t *tray) menu(st trayState) *fyne.Menu {
	conn := fyne.NewMenuItem(st.connection, nil)
	conn.Disabled = true
	items := []*fyne.MenuItem{conn}
	if st.secret != "" {
		items = append(items, fyne.NewMenuItem(st.secret, t.a.focusSecret))
	}
	items = append(items, fyne.NewMenuItemSeparator())

	wifiOn := st.wifiOn
	wifi := fyne.NewMenuItem("Wi-Fi", func() {
		t.a.bg(func(ctx context.Context) {
			_ = t.a.c.SetWifiEnabled(ctx, !wifiOn)
		})
	})
	wifi.Checked = wifiOn
	wifi.Disabled = !st.wifiHW
	items = append(items, wifi)

	if len(st.vpns) > 0 {
		items = append(items, fyne.NewMenuItemSeparator())
	}
	for _, v := range st.vpns {
		id := v.ID
		on := v.State == core.VPNConnected || v.State == core.VPNConnecting
		item := fyne.NewMenuItem(v.Name+"  ("+v.Kind+")", func() {
			t.a.bg(func(ctx context.Context) {
				if on {
					_ = t.a.c.DisconnectVPN(ctx, id)
				} else {
					_ = t.a.c.ConnectVPN(ctx, id)
				}
			})
		})
		item.Checked = on
		item.Disabled = !v.Writable || v.State == core.VPNUnavailable
		items = append(items, item)
	}

	quality := fyne.NewMenuItem("Quality: "+st.verdict, nil)
	quality.Disabled = true
	open := fyne.NewMenuItem("Open bnm", func() {
		t.a.win.Show()
		t.a.win.RequestFocus()
	})
	quit := fyne.NewMenuItem("Quit", t.a.Quit)
	quit.IsQuit = true
	items = append(items, fyne.NewMenuItemSeparator(), quality, fyne.NewMenuItemSeparator(), open, quit)
	return fyne.NewMenu("bnm", items...)
}
