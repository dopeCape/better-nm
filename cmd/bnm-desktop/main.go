// Command bnm-desktop is the bnm desktop app: a Fyne window over bnmd, with a
// tray icon when the desktop has a StatusNotifierItem host. It connects to the
// daemon socket (BNM_SOCKET or $XDG_RUNTIME_DIR/bnm/bnmd.sock), starting bnmd
// on demand when it is not running.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"fyne.io/fyne/v2/app"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/paths"
	"github.com/dopeCape/better-nm/internal/ui/desktop"
	"github.com/dopeCape/better-nm/internal/version"
)

func main() {
	fs := flag.NewFlagSet("bnm-desktop", flag.ExitOnError)
	socket := fs.String("socket", paths.Socket(), "daemon socket (env BNM_SOCKET)")
	noStart := fs.Bool("no-start", false, "do not start bnmd when it is not running")
	tray := fs.String("tray", "auto", "auto|on|off: show a tray icon")
	logLevel := fs.String("log-level", "info", "debug|info|warn|error")
	section := fs.String("section", "", "open on this section (overview|wifi|vpn|quality|speed|devices|advanced|settings)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Printf("bnm-desktop %s (api v%d)\n", version.Version, version.APIVersion)
		return
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	c, err := client.New(client.WithSocket(*socket), client.WithAutoStart(!*noStart), client.WithLogger(logger))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()

	opts := desktop.Options{Logger: logger}
	switch *tray {
	case "on":
		t := true
		opts.Tray = &t
	case "off":
		f := false
		opts.Tray = &f
	}
	fy := app.NewWithID("io.github.dopecape.bnm")
	a := desktop.New(fy, c, opts)
	if *section != "" {
		s, ok := desktop.ParseSection(*section)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown section %q\n", *section)
			os.Exit(2)
		}
		a.Select(s)
	}
	a.Run()
}
