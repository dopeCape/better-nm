package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// Every read of the daemon is a tea.Cmd that ends in one of these messages,
// so the UI never blocks on a request. Actions (connect, toggle, ...) end in
// an actionMsg naming what was attempted.

const requestTimeout = 15 * time.Second

type (
	statusMsg struct {
		st  api.StatusResponse
		err error
	}
	wifiMsg struct {
		nets []core.WifiNetwork
		err  error
	}
	devicesMsg struct {
		devs []core.Device
		err  error
	}
	profilesMsg struct {
		profiles []core.Profile
		err      error
	}
	vpnMsg struct {
		vpns []core.VPN
		err  error
	}
	monitorMsg struct {
		st  core.MonitorStatus
		err error
	}
	samplesMsg struct {
		anchor  string
		samples []core.Sample
		err     error
	}
	speedHistoryMsg struct {
		results []core.SpeedResult
		err     error
	}
	eventHistoryMsg struct {
		events []core.Event
		err    error
	}
	lanMsg struct {
		hosts []core.LANHost
		err   error
	}
	portsMsg struct {
		ports []core.ListeningPort
		err   error
	}
	routesMsg struct {
		routes []core.Route
		err    error
	}
	infraMsg struct {
		nets []core.InfraNetwork
		err  error
	}
	publicIPMsg struct {
		ip  core.PublicIP
		err error
	}
	dnsMsg struct {
		answer core.DNSAnswer
		err    error
	}
	// actionMsg is the outcome of a write; what is a short label ("connect
	// HomeNet"), extra carries a payload some actions return (a login URL).
	actionMsg struct {
		tab   int
		what  string
		extra string
		err   error
	}
)

// loader bundles the client and the program context for the load commands.
type loader struct {
	c   *client.Client
	ctx context.Context
}

func (l loader) with() (context.Context, context.CancelFunc) {
	return context.WithTimeout(l.ctx, requestTimeout)
}

func (l loader) status() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		st, err := l.c.Status(ctx)
		return statusMsg{st, err}
	}
}

func (l loader) wifi() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		nets, err := l.c.Wifi(ctx, "")
		return wifiMsg{nets, err}
	}
}

func (l loader) devices() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		devs, err := l.c.Devices(ctx)
		return devicesMsg{devs, err}
	}
}

func (l loader) profiles() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		ps, err := l.c.Profiles(ctx)
		return profilesMsg{ps, err}
	}
}

func (l loader) vpns() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		vs, err := l.c.VPNs(ctx)
		return vpnMsg{vs, err}
	}
}

func (l loader) monitor() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		st, err := l.c.Monitor(ctx)
		return monitorMsg{st, err}
	}
}

func (l loader) samples(anchor string, limit int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		s, err := l.c.Samples(ctx, "", anchor, limit)
		return samplesMsg{anchor, s, err}
	}
}

func (l loader) speedHistory() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		r, err := l.c.SpeedHistory(ctx, "", 20)
		return speedHistoryMsg{r, err}
	}
}

func (l loader) eventHistory() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		evs, err := l.c.EventHistory(ctx, eventLogSize)
		return eventHistoryMsg{evs, err}
	}
}

func (l loader) lan(device string, sweep bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
		defer cancel()
		h, err := l.c.LANHosts(ctx, device, sweep)
		return lanMsg{h, err}
	}
}

func (l loader) ports() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		p, err := l.c.ListeningPorts(ctx)
		return portsMsg{p, err}
	}
}

func (l loader) routes() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		r, err := l.c.Routes(ctx)
		return routesMsg{r, err}
	}
}

func (l loader) infra() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		n, err := l.c.Infra(ctx)
		return infraMsg{n, err}
	}
}

func (l loader) publicIP() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		ip, err := l.c.PublicIP(ctx)
		return publicIPMsg{ip, err}
	}
}

func (l loader) dns(name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		a, err := l.c.DNSLookup(ctx, name, "", "")
		return dnsMsg{a, err}
	}
}

// action wraps a write in a Cmd that reports as actionMsg.
func (l loader) action(tab int, what string, fn func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
		defer cancel()
		return actionMsg{tab: tab, what: what, err: fn(ctx)}
	}
}

// actionWith is action for writes that return a string (a login URL).
func (l loader) actionWith(tab int, what string, fn func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
		defer cancel()
		s, err := fn(ctx)
		return actionMsg{tab: tab, what: what, extra: s, err: err}
	}
}
