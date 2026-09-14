// Package cli is the `bnm` command line: a cobra command tree over
// internal/client that renders tables and one-screen summaries for humans and
// the raw daemon structs with --json. Nothing here talks to NetworkManager
// directly; every command is a thin client of bnmd. The TUI is reached through
// the RunTUI hook so this package never imports it. Tested by running every
// command in-process against internal/api + internal/daemon + internal/fake
// over a temp Unix socket and asserting on the rendered text and the JSON.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/paths"
	"github.com/dopeCape/better-nm/internal/version"
)

// RunTUI, when set by cmd/bnm, runs the terminal UI. It is called by
// `bnm tui` and by a bare `bnm`; when nil both print the help instead.
var RunTUI func(ctx context.Context, c *client.Client) error

// Exit codes.
const (
	ExitOK          = 0
	ExitError       = 1 // anything the daemon or the CLI refused
	ExitUsage       = 2 // bad flags or arguments
	ExitUnreachable = 3 // bnmd not running / not reachable / wrong API version
	ExitPermission  = 4 // polkit or operator denied
)

// Run executes bnm with args (without the program name) and returns the exit
// code. Output goes to out, diagnostics to errw; in feeds --ask prompts.
func Run(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int {
	// Everything printed for humans passes through a filter that keeps our
	// colour codes and drops any other terminal control (see safe.go); the
	// raw writers stay for TTY detection and the progress bar's own cursor
	// moves.
	sout, serr := newSafeWriter(out), newSafeWriter(errw)
	defer sout.Flush()
	defer serr.Flush()
	a := newApp(in, sout, serr)
	a.raw = out
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errw)
	err := root.ExecuteContext(ctx)
	if a.c != nil {
		_ = a.c.Close()
	}
	if err == nil {
		return ExitOK
	}
	var se *silentExit
	if errors.As(err, &se) {
		return se.code
	}
	code := exitCodeFor(err)
	if errors.Is(err, errHelpShown) {
		return code
	}
	if code == ExitUsage {
		fmt.Fprintf(serr, "error: %s\n", strings.TrimPrefix(err.Error(), "error: "))
		c := a.usageCmd
		if c == nil {
			c = root
		}
		fmt.Fprintf(serr, "usage: %s\n", c.UseLine())
		fmt.Fprintf(serr, "hint: run `%s --help`\n", c.CommandPath())
		return code
	}
	msg := errorMessage(err)
	fmt.Fprintf(serr, "error: %s\n", msg)
	if hint := hintFor(err); hint != "" {
		fmt.Fprintf(serr, "hint: %s\n", hint)
	}
	return code
}

// app is one invocation: global flags, IO and the lazily connected client.
type app struct {
	in   io.Reader
	out  io.Writer // filtered stdout (safeWriter)
	errw io.Writer // filtered stderr
	raw  io.Writer // stdout as given: for TTY detection and the progress bar

	jsonOut     bool
	noColor     bool
	socket      string
	noAutostart bool
	quiet       bool

	ui       *ui
	c        *client.Client
	usageCmd *cobra.Command
	stdin    *lineReader
}

// lines is the prompt reader over stdin (one buffer for the whole run).
func (a *app) lines() *lineReader {
	if a.stdin == nil {
		a.stdin = newLineReader(a.in)
	}
	return a.stdin
}

func newApp(in io.Reader, out, errw io.Writer) *app {
	return &app{in: in, out: out, errw: errw}
}

// errHelpShown marks "help was printed; nothing else to do".
var errHelpShown = errors.New("help shown")

// usageError is a bad invocation: exit 2 and the command's usage line.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// exitCodeFor maps an error to the process exit code.
func exitCodeFor(err error) int {
	var ue *usageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	if errors.Is(err, errHelpShown) {
		return ExitOK
	}
	if client.IsNotRunning(err) {
		return ExitUnreachable
	}
	var vm *client.ErrVersionMismatch
	if errors.As(err, &vm) {
		return ExitUnreachable
	}
	if core.KindOf(err) == core.KindPermission {
		return ExitPermission
	}
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "flag needs an argument", "invalid argument", "accepts ", "requires "} {
		if strings.HasPrefix(msg, p) {
			return ExitUsage
		}
	}
	return ExitError
}

// errorMessage renders an error for humans: the API message without the
// "(hint)" suffix the client appends, and without a redundant "client:" prefix.
func errorMessage(err error) string {
	var ae *client.APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	var nr *client.ErrNotRunning
	if errors.As(err, &nr) {
		return fmt.Sprintf("bnmd is not running (socket %s)", nr.Socket)
	}
	return strings.TrimPrefix(err.Error(), "client: ")
}

func hintFor(err error) string {
	var ae *client.APIError
	if errors.As(err, &ae) {
		return ae.Hint
	}
	if client.IsNotRunning(err) {
		return "run `bnm daemon start`, or drop --no-autostart"
	}
	return core.HintOf(err)
}

// client connects (and auto-starts bnmd unless --no-autostart) on first use.
func (a *app) client() (*client.Client, error) {
	if a.c != nil {
		return a.c, nil
	}
	c, err := client.New(
		client.WithSocket(a.socket),
		client.WithAutoStart(!a.noAutostart),
		client.WithLogger(slog.New(slog.DiscardHandler)),
	)
	if err != nil {
		return nil, err
	}
	if c.StartedDaemon() && !a.quiet && !a.jsonOut {
		fmt.Fprintln(a.errw, "started bnmd")
	}
	a.c = c
	return c, nil
}

// rootCmd builds the whole command tree.
func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "bnm",
		Short: "A better front end for NetworkManager: Wi-Fi, VPNs, monitoring and diagnostics",
		Long: `bnm is the command line for the bnm daemon (bnmd). Run it without arguments
for the terminal UI, or use a subcommand. Every command that prints data
accepts --json for the raw structs. Exit codes: 1 error, 2 usage, 3 daemon
unreachable, 4 permission denied.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if a.socket == "" {
				a.socket = paths.Socket()
			}
			if os.Getenv("NO_COLOR") != "" {
				a.noColor = true
			}
			a.ui = newUI(a.raw, a.noColor)
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if RunTUI == nil {
				return cmd.Help()
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			return RunTUI(cmd.Context(), c)
		},
	}
	root.SetVersionTemplate("bnm {{.Version}}\n")
	root.SetHelpCommand(&cobra.Command{Use: "no-help", Hidden: true})
	root.CompletionOptions.HiddenDefaultCmd = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		a.usageCmd = cmd
		return &usageError{msg: err.Error()}
	})

	pf := root.PersistentFlags()
	pf.BoolVar(&a.jsonOut, "json", false, "print the raw daemon structs as JSON")
	pf.BoolVar(&a.noColor, "no-color", false, "disable colour (NO_COLOR is honoured too)")
	pf.StringVar(&a.socket, "socket", "", "bnmd socket path (default $XDG_RUNTIME_DIR/bnm/bnmd.sock, or $BNM_SOCKET)")
	pf.BoolVar(&a.noAutostart, "no-autostart", false, "do not start bnmd when it is not running")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "print nothing but errors for actions")

	root.AddCommand(
		a.tuiCmd(),
		a.statusCmd(),
		a.devicesCmd(),
		a.wifiCmd(),
		a.wiredCmd(),
		a.profileCmd(),
		a.vpnCmd(),
		a.monitorCmd(),
		a.speedCmd(),
		a.eventsCmd(),
		a.diagCmd(),
		a.configCmd(),
		a.secretsCmd(),
		a.notifyCmd(),
		a.daemonCmd(),
		a.versionCmd(),
	)
	a.markUsage(root)
	return root
}

// markUsage makes every "group" command (one with children and no action)
// print its help and exit 2 when called with a bad or missing subcommand.
func (a *app) markUsage(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		a.markUsage(c)
	}
	if cmd.HasSubCommands() && cmd.RunE == nil && cmd.Run == nil {
		cmd.DisableFlagsInUseLine = true
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				a.usageCmd = cmd
				return usagef("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			_ = cmd.Help()
			return errHelpShown
		}
	}
}

func (a *app) tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the terminal UI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if RunTUI == nil {
				return errors.New("the terminal UI is not built into this binary")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			return RunTUI(cmd.Context(), c)
		},
	}
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the bnm version and, when reachable, the daemon's",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			type out struct {
				Version       string `json:"version"`
				APIVersion    int    `json:"api_version"`
				DaemonVersion string `json:"daemon_version,omitempty"`
				DaemonAPI     int    `json:"daemon_api_version,omitempty"`
			}
			o := out{Version: version.Version, APIVersion: version.APIVersion}
			c, err := client.New(client.WithSocket(a.socket), client.WithAutoStart(false), client.WithVersionCheck(false), client.WithLogger(slog.New(slog.DiscardHandler)))
			if err == nil {
				defer c.Close()
				if st, err := c.Status(cmd.Context()); err == nil {
					o.DaemonVersion, o.DaemonAPI = st.Version, st.APIVersion
				}
			}
			if a.jsonOut {
				return a.printJSON(o)
			}
			fmt.Fprintf(a.out, "bnm %s (api v%d)\n", o.Version, o.APIVersion)
			if o.DaemonVersion != "" {
				fmt.Fprintf(a.out, "bnmd %s (api v%d)\n", o.DaemonVersion, o.DaemonAPI)
			} else {
				fmt.Fprintln(a.out, "bnmd not running")
			}
			return nil
		},
	}
}

// exactArgs is cobra.ExactArgs that yields a usage error (exit 2).
func (a *app) exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			a.usageCmd = cmd
			return usagef("%s takes %d argument(s), got %d", cmd.CommandPath(), n, len(args))
		}
		return nil
	}
}

// rangeArgs is cobra.RangeArgs that yields a usage error (exit 2).
func (a *app) rangeArgs(lo, hi int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < lo || len(args) > hi {
			a.usageCmd = cmd
			return usagef("%s takes %d to %d argument(s), got %d", cmd.CommandPath(), lo, hi, len(args))
		}
		return nil
	}
}

// noArgs is cobra.NoArgs that yields a usage error (exit 2).
func (a *app) noArgs() cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			a.usageCmd = cmd
			return usagef("%s takes no arguments", cmd.CommandPath())
		}
		return nil
	}
}

// onOff parses on|off (and true/false, yes/no, 1/0).
func (a *app) onOff(cmd *cobra.Command, s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1", "enable", "enabled":
		return true, nil
	case "off", "false", "no", "0", "disable", "disabled":
		return false, nil
	}
	a.usageCmd = cmd
	return false, usagef("want on or off, got %q", s)
}

// done prints a success line for an action unless --quiet or --json.
func (a *app) done(format string, args ...any) {
	if a.quiet {
		return
	}
	if a.jsonOut {
		_ = a.printJSON(map[string]any{"ok": true, "message": fmt.Sprintf(format, args...)})
		return
	}
	fmt.Fprintf(a.out, format+"\n", args...)
}
