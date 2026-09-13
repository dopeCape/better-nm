package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
)

// notifyKeys are the per-event notification toggles, in display order.
var notifyKeys = []struct {
	key   string
	label string
	get   func(config.Notify) bool
}{
	{"notify.connected", "Connected to a network", func(n config.Notify) bool { return n.Connected }},
	{"notify.disconnected", "Disconnected", func(n config.Notify) bool { return n.Disconnected }},
	{"notify.no_internet", "No internet", func(n config.Notify) bool { return n.NoInternet }},
	{"notify.internet_restored", "Internet restored", func(n config.Notify) bool { return n.InternetRestored }},
	{"notify.vpn_up", "VPN up", func(n config.Notify) bool { return n.VPNUp }},
	{"notify.vpn_down", "VPN down", func(n config.Notify) bool { return n.VPNDown }},
	{"notify.degraded", "Quality degraded", func(n config.Notify) bool { return n.Degraded }},
	{"notify.recovered", "Quality recovered", func(n config.Notify) bool { return n.Recovered }},
}

type settingsView struct {
	a   *App
	gen atomic.Int64

	guard    bool
	loadErr  *errorLabel
	notifErr *errorLabel
	checks   map[string]*widget.Check
	muted    *widget.Entry
	interval *widget.Entry
	anchors  *widget.Entry
	monErr   *errorLabel
	provider *widget.Select
	iperf    *widget.Entry
	speedErr *errorLabel
	testBtn  *widget.Button
	testOut  *widget.Label
	daemon   *kv
	install  *widget.Button
	instOut  *widget.Label
	instErr  *errorLabel
}

func newSettingsView(a *App) *settingsView {
	return &settingsView{a: a, checks: map[string]*widget.Check{}}
}

func (v *settingsView) build() fyne.CanvasObject {
	v.loadErr = newErrorLabel()
	v.notifErr = newErrorLabel()
	notif := container.NewVBox()
	for _, nk := range notifyKeys {
		key := nk.key
		c := widget.NewCheck(nk.label, func(on bool) { v.setKey(key, fmt.Sprint(on), v.notifErr) })
		v.checks[key] = c
		notif.Add(c)
	}
	v.muted = widget.NewEntry()
	v.muted.SetPlaceHolder("wifi:CoffeeShop, wired:...")
	mutedApply := widget.NewButton("Save muted networks", func() { v.setKey("notify.muted_networks", v.muted.Text, v.notifErr) })
	v.testBtn = widget.NewButton("Send test notification", v.sendTest)
	v.testOut = caption("")
	notifBox := container.NewVBox(
		notif,
		inset(widget.NewForm(widget.NewFormItem("Muted networks", v.muted)), 8, 0, 0, 0),
		inset(container.NewHBox(mutedApply, v.testBtn, v.testOut), 8, 0, 0, 0),
		v.notifErr,
	)

	v.interval = widget.NewEntry()
	v.interval.SetPlaceHolder("30s")
	v.anchors = widget.NewEntry()
	v.anchors.SetPlaceHolder("1.1.1.1, 8.8.8.8")
	v.monErr = newErrorLabel()
	monApply := widget.NewButton("Save monitor settings", func() {
		v.setKey("monitor.interval", strings.TrimSpace(v.interval.Text), v.monErr)
		v.setKey("monitor.anchors", v.anchors.Text, v.monErr)
	})
	monBox := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Probe interval", v.interval),
			widget.NewFormItem("Anchors", v.anchors),
		),
		inset(container.NewHBox(monApply), 8, 0, 0, 0),
		v.monErr,
	)

	v.provider = widget.NewSelect([]string{"cloudflare", "iperf3", "librespeed"}, func(p string) {
		if v.guard {
			return
		}
		v.setKey("speed.provider", p, v.speedErr)
	})
	v.iperf = widget.NewEntry()
	v.iperf.SetPlaceHolder("host:5201")
	v.iperf.OnSubmitted = func(s string) { v.setKey("speed.iperf3_server", s, v.speedErr) }
	v.speedErr = newErrorLabel()
	speedBox := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Provider", v.provider),
			widget.NewFormItem("iperf3 server", v.iperf),
		),
		caption("Press Enter in the server field to save it."),
		v.speedErr,
	)

	v.daemon = newKV("Version", "Uptime", "Socket", "NetworkManager")
	v.install = widget.NewButton("Install as user service", v.installService)
	v.instOut = caption("")
	v.instOut.Wrapping = fyne.TextWrapWord
	v.instErr = newErrorLabel()
	note := caption("The daemon starts on demand; installing it as a systemd user service keeps monitoring and notifications running without the app.")
	note.Wrapping = fyne.TextWrapWord
	daemonBox := container.NewVBox(
		v.daemon.box,
		note,
		inset(container.NewHBox(v.install), 8, 0, 0, 0),
		v.instOut,
		v.instErr,
	)

	left := container.NewVBox(section("Notifications", notifBox), section("Monitor", monBox))
	right := container.NewVBox(section("Speed test", speedBox), section("Daemon", daemonBox))
	grid := container.NewGridWithColumns(2, inset(left, 0, 16, 0, 0), inset(right, 0, 0, 0, 16))
	return page(container.NewVBox(v.loadErr, grid))
}

func (v *settingsView) refresh() {
	gen := v.gen.Add(1)
	var cfg config.Config
	var st api.StatusResponse
	var loadErr error
	v.a.apply(func(ctx context.Context) error {
		if cfg, loadErr = v.a.c.Config(ctx); loadErr != nil {
			return loadErr
		}
		st, _ = v.a.c.Status(ctx)
		return nil
	}, func() {
		if gen != v.gen.Load() {
			return
		}
		v.loadErr.set(loadErr)
		if loadErr != nil {
			return
		}
		v.render(cfg, st)
	})
}

func (v *settingsView) render(cfg config.Config, st api.StatusResponse) {
	v.guard = true
	defer func() { v.guard = false }()
	for _, nk := range notifyKeys {
		c := v.checks[nk.key]
		want := nk.get(cfg.Notify)
		if c.Checked != want {
			setCheckedSilently(c, want)
		}
	}
	v.muted.SetText(strings.Join(cfg.Notify.MutedNetworks, ", "))
	v.interval.SetText(cfg.Monitor.Interval.String())
	v.anchors.SetText(strings.Join(cfg.Monitor.Anchors, ", "))
	v.provider.SetSelected(cfg.Speed.Provider)
	v.iperf.SetText(cfg.Speed.Iperf3Server)
	uptime := ""
	if st.UptimeSeconds > 0 {
		uptime = fmtDur(time.Duration(st.UptimeSeconds * float64(time.Second)))
	}
	v.daemon.setAll(st.Version, uptime, v.a.c.Socket(), st.NMVersion)
}

// setKey writes one config key; errors land on the given label.
func (v *settingsView) setKey(key, value string, errLabel *errorLabel) {
	errLabel.set(nil)
	v.a.bg(func(ctx context.Context) {
		if _, err := v.a.c.SetConfig(ctx, key, value); err != nil {
			v.a.onUI(func() { errLabel.set(err) })
			return
		}
		v.refresh()
	})
}

func (v *settingsView) sendTest() {
	v.notifErr.set(nil)
	v.testOut.SetText("")
	v.a.bg(func(ctx context.Context) {
		err := v.a.c.NotifyTest(ctx)
		v.a.onUI(func() {
			if err != nil {
				v.notifErr.set(err)
				return
			}
			v.testOut.SetText("sent")
		})
	})
}

func (v *settingsView) installService() {
	v.instErr.set(nil)
	v.instOut.SetText("Installing")
	v.install.Disable()
	fn := v.a.opts.InstallService
	if fn == nil {
		fn = InstallUserService
	}
	v.a.bg(func(ctx context.Context) {
		msg, err := fn(ctx)
		v.a.onUI(func() {
			v.install.Enable()
			v.instErr.set(err)
			v.instOut.SetText(msg)
		})
	})
}

// --- user service ------------------------------------------------------------------

// UnitText renders the systemd user unit for a bnmd binary at path.
func UnitText(bnmdPath string) string {
	return "[Unit]\n" +
		"Description=bnm network daemon\n" +
		"Documentation=https://github.com/dopeCape/better-nm\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"ExecStart=" + bnmdPath + "\n" +
		"Restart=on-failure\n" +
		"RestartSec=2\n\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
}

// UserUnitPath is where the user unit is written: $XDG_CONFIG_HOME/systemd/user/bnmd.service.
func UserUnitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", "bnmd.service")
}

// InstallUserService does what `bnm daemon install` does: write the user unit
// pointing at the bnmd next to this binary (or on PATH) and run
// `systemctl --user enable --now bnmd.service`. It never escalates.
func InstallUserService(ctx context.Context) (string, error) {
	bin, err := client.FindDaemon()
	if err != nil {
		return "", core.Wrap(core.KindNotFound, "install bnmd next to bnm-desktop or on PATH", err)
	}
	path := UserUnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("desktop: unit dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(UnitText(bin)), 0o644); err != nil {
		return "", fmt.Errorf("desktop: write unit: %w", err)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", core.Errorf(core.KindUnsupported, "this desktop has no systemd user session; start bnmd from your session manager instead", "wrote %s but systemctl is not available", path)
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", "bnmd.service"}} {
		cmd := exec.CommandContext(ctx, "systemctl", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", core.Wrap(core.KindInternal, strings.TrimSpace(string(out)), fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err))
		}
	}
	return "Installed " + path + " and enabled bnmd.service. If a bnmd started on demand still owns the socket, the service takes over once that one exits (or at next login).", nil
}
