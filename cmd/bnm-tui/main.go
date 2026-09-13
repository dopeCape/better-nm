// Command bnm-tui is the development entry point for the TUI: it connects to
// bnmd (BNM_SOCKET overrides the socket path; the daemon is auto-started when
// absent unless BNM_NO_AUTOSTART is set) and runs internal/ui/tui. The real
// user-facing entry is `bnm` with no arguments.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/ui/tui"
)

func main() {
	var opts []client.Option
	if s := os.Getenv("BNM_SOCKET"); s != "" {
		opts = append(opts, client.WithSocket(s))
	}
	if os.Getenv("BNM_NO_AUTOSTART") != "" {
		opts = append(opts, client.WithAutoStart(false))
	}
	c, err := client.New(opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bnm-tui:", err)
		os.Exit(1)
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := tui.Run(ctx, c); err != nil {
		fmt.Fprintln(os.Stderr, "bnm-tui:", err)
		os.Exit(1)
	}
}
