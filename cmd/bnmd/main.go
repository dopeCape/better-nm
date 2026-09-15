// Command bnmd is the bnm daemon: one per user session, listening on a Unix
// socket, owning the NetworkManager subscription, VPN adapters, the monitor,
// history and the event stream. `bnmd --fake` serves an in-memory world so the
// CLI, TUI and desktop app can be developed without NetworkManager.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
	"github.com/dopeCape/better-nm/internal/diag"
	"github.com/dopeCape/better-nm/internal/fake"
	"github.com/dopeCape/better-nm/internal/infra"
	"github.com/dopeCape/better-nm/internal/monitor"
	"github.com/dopeCape/better-nm/internal/nm"
	"github.com/dopeCape/better-nm/internal/notify"
	"github.com/dopeCape/better-nm/internal/paths"
	"github.com/dopeCape/better-nm/internal/speed"
	"github.com/dopeCape/better-nm/internal/store"
	"github.com/dopeCape/better-nm/internal/version"
	"github.com/dopeCape/better-nm/internal/vpn"
	"github.com/dopeCape/better-nm/internal/vpn/nmvpn"
	"github.com/dopeCape/better-nm/internal/vpn/tailscale"
	"github.com/dopeCape/better-nm/internal/vpn/wireguard"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("bnmd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", daemon.DefaultSocketPath(), "Unix socket to listen on")
	logLevel := fs.String("log-level", "", "debug|info|warn|error (default: config daemon.log_level)")
	cfgPath := fs.String("config", config.Path(), "config file")
	showVersion := fs.Bool("version", false, "print the version and exit")
	useFake := fs.Bool("fake", false, "serve an in-memory fake world instead of NetworkManager")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "bnmd %s (api v%d)\n", version.Version, version.APIVersion)
		return 0
	}

	cfg, err := config.LoadFrom(*cfgPath)
	var unknown *config.UnknownKeysError
	if err != nil && !errors.As(err, &unknown) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	level := cfg.Daemon.LogLevel
	if *logLevel != "" {
		level = *logLevel
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: parseLevel(level)}))
	slog.SetDefault(logger)
	if unknown != nil {
		logger.Warn("config has unknown keys", "keys", strings.Join(unknown.Keys, ","), "path", *cfgPath)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var opts daemon.Options
	if *useFake {
		opts = fakeOptions()
	} else {
		opts, err = realOptions(ctx, cfg, logger)
		if err != nil {
			logger.Error("cannot start", "err", err)
			return 1
		}
	}
	opts.Config = cfg
	opts.ConfigPath = *cfgPath
	opts.Logger = logger
	opts.Version = version.Version

	d, err := daemon.New(opts)
	if err != nil {
		logger.Error("cannot build daemon", "err", err)
		return 1
	}
	defer d.Close()

	sock, err := daemon.Listen(*socket)
	if err != nil {
		var already *daemon.ErrAlreadyRunning
		if errors.As(err, &already) {
			logger.Error(already.Error())
			return 3
		}
		logger.Error("cannot listen", "err", err)
		return 1
	}
	defer sock.Close()
	logger.Info("listening", "socket", *socket, "fake", *useFake)

	srv := api.New(d, api.WithLogger(logger))
	errCh := make(chan error, 2)
	go func() { errCh <- d.Run(ctx) }()
	go func() { errCh <- srv.Serve(ctx, sock.Listener()) }()
	first := <-errCh
	stop()
	second := <-errCh
	for _, e := range []error{first, second} {
		if e != nil && !errors.Is(e, context.Canceled) {
			logger.Error("stopped with error", "err", e)
			return 1
		}
	}
	logger.Info("bye")
	return 0
}

// fakeOptions builds the in-memory world served by --fake.
func fakeOptions() daemon.Options {
	nm := fake.NewNM()
	secrets := fake.NewSecretBroker()
	nm.UseSecrets(secrets)
	ovpn := fake.NewNMVPN()
	ovpn.Secrets = secrets
	ts := fake.NewTailscale()
	_ = ts.Connect(context.Background(), "tailscale") // a lived-in world: Tailscale up
	mon := fake.NewMonitor()
	st := fake.NewStore()
	seedFakeHistory(mon, st)
	return daemon.Options{
		NM:        nm,
		Secrets:   secrets,
		VPN:       fake.NewVPNRegistry(ts, fake.NewWireGuard(), ovpn),
		Monitor:   mon,
		Store:     st,
		Speed:     &fake.SpeedTester{Delay: 300 * time.Millisecond}, // so progress is visible
		Notifier:  fake.NewNotifier(),
		Diag:      fake.NewDiag(),
		WireGuard: &fake.WireGuardImporter{NM: nm},
	}
}

// seedFakeHistory gives the fake world an hour of plausible probe samples with a
// learned baseline, and a few events, so the surfaces have something to draw.
func seedFakeHistory(mon *fake.Monitor, st *fake.Store) {
	const key = "wifi:HomeNet"
	now := time.Now()
	rtt := map[string]float64{"gateway": 1.2, "1.1.1.1": 14.0, "8.8.8.8": 15.5}
	for i := 120; i >= 0; i-- {
		t := now.Add(-time.Duration(i) * 30 * time.Second)
		// a gentle wobble plus one short bump twenty minutes ago
		wobble := float64((i*7)%5) * 0.3
		bump := 0.0
		if i > 38 && i < 44 {
			bump = 9
		}
		for n, anchor := range []string{"gateway", "1.1.1.1", "8.8.8.8"} {
			s := core.Sample{Time: t, NetworkKey: key, Anchor: anchor, RTTms: rtt[anchor] + wobble + bump, Method: "icmp", DNSms: -1}
			if n == 0 {
				s.DNSms = 18 + wobble
			}
			mon.AddSample(s)
			_ = st.AddSample(context.Background(), s)
		}
	}
	mon.SetStatus(core.MonitorStatus{
		NetworkKey: key, State: core.BaselineOK, Interval: 30 * time.Second, LastSample: now,
		Anchors: []core.Baseline{
			{NetworkKey: key, Anchor: "gateway", State: core.BaselineOK, SampleCount: 121, BaselineRTT: 1.2, CurrentRTT: 1.4, CurrentDNS: 18, UpdatedAt: now},
			{NetworkKey: key, Anchor: "1.1.1.1", State: core.BaselineOK, SampleCount: 121, BaselineRTT: 14.0, CurrentRTT: 14.6, UpdatedAt: now},
			{NetworkKey: key, Anchor: "8.8.8.8", State: core.BaselineOK, SampleCount: 121, BaselineRTT: 15.5, CurrentRTT: 15.9, UpdatedAt: now},
		},
	})
	for _, e := range []core.Event{
		{Time: now.Add(-50 * time.Minute), Type: core.EventConnected, NetworkKey: key, Title: "Connected to HomeNet", Body: "192.168.1.42 via wlan0", Urgency: "low"},
		{Time: now.Add(-49 * time.Minute), Type: core.EventVPNUp, Title: "Tailscale connected", Body: "1 peer online", Urgency: "low"},
		{Time: now.Add(-21 * time.Minute), Type: core.EventDegraded, NetworkKey: key, Title: "HomeNet is slower than usual", Body: "Latency to 1.1.1.1 is 23 ms, usually 14 ms", Urgency: "normal"},
		{Time: now.Add(-16 * time.Minute), Type: core.EventRecovered, NetworkKey: key, Title: "HomeNet is back to normal", Body: "Round-trip back within baseline", Urgency: "low"},
	} {
		_ = st.AddEvent(context.Background(), e)
	}
}

// realOptions wires the production backends. Everything runs unprivileged: NM over
// the system bus with polkit, Tailscale via its LocalAPI socket, WireGuard and plugin
// VPNs as NM profiles, probes on ping sockets (TCP fallback), history in SQLite.
func realOptions(ctx context.Context, cfg config.Config, logger *slog.Logger) (daemon.Options, error) {
	nmClient, err := nm.New(ctx, nm.WithLogger(logger))
	if err != nil {
		return daemon.Options{}, fmt.Errorf("connect to NetworkManager: %w", err)
	}
	stateDir := paths.StateDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return daemon.Options{}, fmt.Errorf("state dir: %w", err)
	}
	retention := time.Duration(cfg.Monitor.RetentionDays) * 24 * time.Hour
	st, err := store.Open(filepath.Join(stateDir, "bnm.db"), store.WithRetention(retention))
	if err != nil {
		return daemon.Options{}, fmt.Errorf("open store: %w", err)
	}

	ts := tailscale.NewAdapter(tailscale.New(cfg.Tailscale.Socket), logger)
	wg := wireguard.NewAdapter(nmClient, logger)
	ov := nmvpn.NewAdapter(nmClient, logger)
	reg := vpn.NewRegistry(ts, wg, ov)

	mon := monitor.New(monitor.Config{
		Interval: cfg.Monitor.Interval,
		Anchors:  cfg.Monitor.Anchors,
	}, st, monitor.NewAutoProber(logger), logger)

	nt := &policyNotifier{Notifier: notify.New(policyFromConfig(cfg.Notify), logger)}

	if nmClient.AgentRegistered() {
		logger.Info("secret agent ready: NetworkManager will prompt through bnm", "identifier", nm.AgentIdentifier)
	} else {
		logger.Warn("secret agent not registered: activations needing a password the profile does not store will fail")
	}

	return daemon.Options{
		NM:      nmClient,
		Secrets: nmClient,
		Store:   st,
		VPN:     reg,
		Monitor: monitorRunner{mon},
		// The daemon fills SpeedOptions.Provider from the config when a request
		// leaves it empty, and a request may name another provider; the
		// dispatcher picks the tester per call.
		Speed:     speed.NewDispatcher(),
		Notifier:  nt,
		WireGuard: wgImporter{wg},
		Diag: daemon.DiagFuncs{
			LANHostsFn:       diag.LANHosts,
			ListeningPortsFn: diag.ListeningPorts,
			RoutesFn:         diag.Routes,
			DNSLookupFn:      diag.DNSLookup,
			PublicIPFn:       diag.PublicIP,
			InfraFn:          infra.Networks,
		},
	}, nil
}

// policyFromConfig maps the TOML notification section onto the notifier's policy.
func policyFromConfig(n config.Notify) notify.Policy {
	p := notify.DefaultPolicy()
	p.Enabled = map[core.EventType]bool{
		core.EventConnected:        n.Connected,
		core.EventDisconnected:     n.Disconnected,
		core.EventNoInternet:       n.NoInternet,
		core.EventInternetRestored: n.InternetRestored,
		core.EventVPNUp:            n.VPNUp,
		core.EventVPNDown:          n.VPNDown,
		core.EventDegraded:         n.Degraded,
		core.EventRecovered:        n.Recovered,
		core.EventSecretNeeded:     n.SecretNeeded,
	}
	p.MutedNetworks = append([]string(nil), n.MutedNetworks...)
	return p
}

// policyNotifier lets PUT /config swap the policy at runtime (daemon.PolicyUpdater).
type policyNotifier struct {
	mu sync.Mutex
	*notify.Notifier
	logger *slog.Logger
}

func (p *policyNotifier) Notify(ctx context.Context, e core.Event) error {
	p.mu.Lock()
	n := p.Notifier
	p.mu.Unlock()
	return n.Notify(ctx, e)
}

// Deliver bypasses the filter (used by POST /notify/test).
func (p *policyNotifier) Deliver(ctx context.Context, e core.Event) error {
	p.mu.Lock()
	n := p.Notifier
	p.mu.Unlock()
	return n.Deliver(ctx, e)
}

func (p *policyNotifier) SetPolicy(n config.Notify) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Notifier.Filter().Flush()
	p.Notifier = notify.New(policyFromConfig(n), p.logger)
}

// monitorRunner adapts monitor.Monitor.Run (no error) to daemon.Monitor.
type monitorRunner struct{ *monitor.Monitor }

func (m monitorRunner) Run(ctx context.Context) error {
	m.Monitor.Run(ctx)
	return nil
}

// wgImporter adapts wireguard.Adapter.Import (which also returns the parsed spec)
// to the daemon's narrower interface.
type wgImporter struct{ a *wireguard.Adapter }

func (w wgImporter) Import(ctx context.Context, name string, conf io.Reader) (string, error) {
	uuid, _, err := w.a.Import(ctx, name, conf)
	return uuid, err
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
