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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The TUI registers itself here when it is built in:
	//   cli.RunTUI = tui.Run
	// With RunTUI nil, `bnm` and `bnm tui` print the help.
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
