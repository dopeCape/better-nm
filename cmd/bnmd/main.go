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
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
	"github.com/dopeCape/better-nm/internal/fake"
	"github.com/dopeCape/better-nm/internal/version"
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
	return daemon.Options{
		NM:        nm,
		VPN:       fake.NewVPNRegistry(fake.NewTailscale(), fake.NewWireGuard(), fake.NewNMVPN()),
		Monitor:   fake.NewMonitor(),
		Store:     fake.NewStore(),
		Speed:     &fake.SpeedTester{Delay: 300 * time.Millisecond}, // so progress is visible
		Notifier:  fake.NewNotifier(),
		Diag:      fake.NewDiag(),
		WireGuard: &fake.WireGuardImporter{NM: nm},
	}
}

// realOptions wires the production backends.
//
// WIRE: the orchestrator replaces each stub below with the real constructor once
// the packages have merged. Expected shapes (see internal/daemon for the
// interfaces the daemon codes against):
//
//	nmClient, err := nm.New(ctx)                                  // core.NetworkManager
//	st, err := store.Open(filepath.Join(paths.StateDir(), "bnm.db")) // core.Store
//	ts := tailscale.New(cfg.Tailscale.Socket)                      // core.VPNAdapter + core.TailscaleControl
//	wg := wireguard.New(nmClient)                                  // core.VPNAdapter + Import(ctx, name, io.Reader)
//	ov := nmvpn.New(nmClient)                                      // core.VPNAdapter
//	reg := vpn.NewRegistry(ts, wg, ov)                             // daemon.VPNRegistry (List/Connect/Disconnect/Watch/Tailscale)
//	mon := monitor.New(monitorConfig(cfg), st, prober, logger)     // daemon.Monitor
//	sp := speed.New(cfg.Speed.Provider)                            // core.SpeedTester
//	nt := notify.New(cfg.Notify, logger)                           // core.Notifier (+ optional SetPolicy(config.Notify))
//	dg := daemon.DiagFuncs{LANHostsFn: diag.LANHosts, ListeningPortsFn: diag.ListeningPorts,
//	      RoutesFn: diag.Routes, DNSLookupFn: diag.DNSLookup, PublicIPFn: diag.PublicIP, InfraFn: infra.Networks}
//
// If a real constructor's signature differs from the daemon interface (for
// example Registry.Tailscale returning (core.TailscaleControl, bool), or
// wireguard.Import returning something other than (uuid string, error)), add a
// two-line adapter type here rather than changing the daemon.
func realOptions(ctx context.Context, cfg config.Config, logger *slog.Logger) (daemon.Options, error) {
	_ = ctx
	_ = cfg
	_ = logger
	nmClient, err := newNM(ctx)
	if err != nil {
		return daemon.Options{}, err
	}
	st, err := openStore()
	if err != nil {
		return daemon.Options{}, err
	}
	return daemon.Options{
		NM:    nmClient,
		Store: st,
		// WIRE: VPN, Monitor, Speed, Notifier, Diag, WireGuard go here.
	}, nil
}

// WIRE: replace with nm.New(ctx).
func newNM(ctx context.Context) (core.NetworkManager, error) {
	return nil, errNotWired("internal/nm")
}

// WIRE: replace with store.Open(filepath.Join(paths.StateDir(), "bnm.db")).
func openStore() (core.Store, error) {
	return nil, errNotWired("internal/store")
}

func errNotWired(pkg string) error {
	return core.Errorf(core.KindUnsupported, "run `bnmd --fake` for the in-memory world", "bnmd: %s is not wired yet", pkg)
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
