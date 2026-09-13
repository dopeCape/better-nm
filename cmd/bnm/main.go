// Command bnm is the command line (and, through internal/ui/tui, the terminal
// UI) for bnmd. All the work lives in internal/cli; this file only wires the
// process: signals become context cancellation and the exit code is the CLI's.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dopeCape/better-nm/internal/cli"
	"github.com/dopeCape/better-nm/internal/ui/tui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// `bnm` with no arguments and `bnm tui` open the terminal UI.
	cli.RunTUI = tui.Run
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
