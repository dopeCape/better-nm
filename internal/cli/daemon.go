package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
	"github.com/dopeCape/better-nm/internal/paths"
)

// unitName is the systemd user unit bnm installs.
const unitName = "bnmd.service"

// stopWait bounds how long `daemon stop` waits for the socket to go away.
const stopWait = 5 * time.Second

func (a *app) daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage bnmd: status, start, stop, install as a service, logs",
	}
	cmd.AddCommand(a.daemonStatusCmd(), a.daemonStartCmd(), a.daemonStopCmd(), a.daemonRestartCmd(),
		a.daemonInstallCmd(), a.daemonUninstallCmd(), a.daemonLogsCmd())
	return cmd
}

// daemonInfo is what `bnm daemon status` reports.
type daemonInfo struct {
	Running    bool    `json:"running"`
	Socket     string  `json:"socket"`
	PID        int     `json:"pid,omitempty"`
	Version    string  `json:"version,omitempty"`
	APIVersion int     `json:"api_version,omitempty"`
	Uptime     float64 `json:"uptime_seconds,omitempty"`
	Unit       string  `json:"unit,omitempty"` // systemd user unit state, "" when systemctl is absent
	UnitFile   string  `json:"unit_file,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// pidFile is the lock file bnmd writes beside its socket.
func pidFile(socket string) string {
	return filepath.Join(filepath.Dir(socket), daemon.LockName)
}

// readPID returns the pid recorded beside the socket when that process is alive.
func readPID(socket string) int {
	data, err := os.ReadFile(pidFile(socket))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return 0
	}
	return pid
}

// probe connects without auto-start or version check.
func (a *app) probe(ctx context.Context) daemonInfo {
	info := daemonInfo{Socket: a.socket, PID: readPID(a.socket)}
	c, err := client.New(client.WithSocket(a.socket), client.WithAutoStart(false), client.WithVersionCheck(false), client.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		info.Error = errorMessage(err)
		return info
	}
	defer c.Close()
	st, err := c.Status(ctx)
	if err != nil {
		info.Error = errorMessage(err)
		return info
	}
	info.Running = true
	info.Version, info.APIVersion, info.Uptime = st.Version, st.APIVersion, st.UptimeSeconds
	return info
}

func unitPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = filepath.Join(paths.ConfigDir(), "..")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

func haveSystemctl() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// unitState is systemctl's word for the unit (active, inactive, failed, ...),
// or "" when systemctl is missing.
func unitState(ctx context.Context) string {
	if !haveSystemctl() {
		return ""
	}
	out, _ := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unitName).Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown"
	}
	return s
}

func (a *app) systemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(buf.String()))
	}
	return nil
}

func (a *app) daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Is bnmd reachable? version, uptime, pid, service state",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			info := a.probe(ctx)
			info.Unit = unitState(ctx)
			if _, err := os.Stat(unitPath()); err == nil {
				info.UnitFile = unitPath()
			}
			if a.jsonOut {
				if err := a.printJSON(info); err != nil {
					return err
				}
			} else {
				u := a.ui
				if info.Running {
					fmt.Fprintf(a.out, "%s %s\n", u.dot("green"), u.bold.Render(fmt.Sprintf("bnmd %s running", info.Version)))
				} else {
					fmt.Fprintf(a.out, "%s %s\n", u.dot(""), u.bold.Render("bnmd not running"))
				}
				k := u.kv()
				k.add("Socket", info.Socket)
				if info.PID > 0 {
					k.add("PID", fmt.Sprint(info.PID))
				}
				if info.Running {
					k.add("API", fmt.Sprintf("v%d", info.APIVersion))
					k.add("Uptime", shortDuration(time.Duration(info.Uptime)*time.Second))
				} else if info.Error != "" {
					k.add("Error", u.dim.Render(info.Error))
				}
				switch info.Unit {
				case "":
					k.add("Service", u.dim.Render("systemctl not found"))
				case "active":
					k.add("Service", u.green.Render("active")+u.dim.Render(" (systemd user unit)"))
				default:
					s := info.Unit
					if info.UnitFile == "" {
						s += u.dim.Render(" (not installed; bnm daemon install)")
					}
					k.add("Service", s)
				}
				k.render(a.out)
			}
			if !info.Running {
				return &silentExit{code: ExitUnreachable}
			}
			return nil
		},
	}
}

// silentExit carries an exit code without printing an error.
type silentExit struct{ code int }

func (e *silentExit) Error() string { return fmt.Sprintf("exit %d", e.code) }

func (a *app) daemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start bnmd detached (or via systemd when the unit is installed)",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if info := a.probe(ctx); info.Running {
				a.done("bnmd %s already running (pid %d)", info.Version, info.PID)
				return nil
			}
			if _, err := os.Stat(unitPath()); err == nil && haveSystemctl() && a.socket == paths.Socket() {
				if err := a.systemctl(ctx, "start", unitName); err != nil {
					return err
				}
			} else {
				bin, err := client.FindDaemon()
				if err != nil {
					return core.Wrap(core.KindNotFound, "install bnmd next to bnm or in PATH", err)
				}
				c, err := client.New(client.WithSocket(a.socket), client.WithAutoStart(true), client.WithDaemonPath(bin), client.WithLogger(slog.New(slog.DiscardHandler)))
				if err != nil {
					return err
				}
				c.Close()
			}
			info := a.probe(ctx)
			if !info.Running {
				return core.Errorf(core.KindUnavailable, "see "+filepath.Join(paths.StateDir(), "bnmd.log"), "bnmd did not come up")
			}
			a.done("%s bnmd %s started (pid %d)", a.ui.dot("green"), info.Version, info.PID)
			return nil
		},
	}
}

func (a *app) daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop bnmd (SIGTERM, or systemctl stop when it runs as a service)",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.stopDaemon(cmd.Context())
		},
	}
}

func (a *app) stopDaemon(ctx context.Context) error {
	info := a.probe(ctx)
	if !info.Running && info.PID == 0 {
		a.done("bnmd is not running")
		return nil
	}
	if unitState(ctx) == "active" && a.socket == paths.Socket() {
		if err := a.systemctl(ctx, "stop", unitName); err != nil {
			return err
		}
	} else {
		if info.PID == 0 {
			return core.Errorf(core.KindNotFound, "kill it by hand: pkill -x bnmd", "bnmd answers on %s but its pid file %s is missing or stale", a.socket, pidFile(a.socket))
		}
		if err := syscall.Kill(info.PID, syscall.SIGTERM); err != nil {
			return core.Wrap(core.KindPermission, "", fmt.Errorf("signal pid %d: %w", info.PID, err))
		}
	}
	deadline := time.Now().Add(stopWait)
	for time.Now().Before(deadline) {
		if p := a.probe(ctx); !p.Running && (info.PID == 0 || syscall.Kill(info.PID, 0) != nil) {
			a.done("bnmd stopped")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return core.Errorf(core.KindInternal, "", "bnmd (pid %d) did not exit within %s", info.PID, stopWait)
}

func (a *app) daemonRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Stop and start bnmd",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.stopDaemon(cmd.Context()); err != nil {
				return err
			}
			return a.daemonStartCmd().RunE(cmd, nil)
		},
	}
}

func (a *app) daemonInstallCmd() *cobra.Command {
	var unitOnly bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install bnmd as a systemd user service and start it",
		Long: `Writes ~/.config/systemd/user/bnmd.service pointing at the bnmd binary next
to this bnm (or in PATH), then runs systemctl --user daemon-reload and
enable --now. A bnmd started by hand is stopped first so the service can
bind the socket. --user-unit only writes the unit file.`,
		Args: a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			bin, err := client.FindDaemon()
			if err != nil {
				return core.Wrap(core.KindNotFound, "install bnmd next to bnm or in PATH", err)
			}
			if abs, err := filepath.Abs(bin); err == nil {
				bin = abs
			}
			path := unitPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
			}
			if err := os.WriteFile(path, []byte(unitFile(bin)), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			if !a.quiet && !a.jsonOut {
				fmt.Fprintf(a.out, "wrote %s (ExecStart=%s)\n", path, bin)
			}
			if unitOnly {
				a.done("unit written; enable it with: systemctl --user daemon-reload && systemctl --user enable --now %s", unitName)
				return nil
			}
			if !haveSystemctl() {
				return core.Errorf(core.KindUnsupported, "the unit file is in place; start bnmd another way (bnm daemon start) or run: systemctl --user enable --now "+unitName, "systemctl not found")
			}
			// A hand-started daemon holds the socket lock; stop it so the unit can start.
			if info := a.probe(ctx); info.Running && unitState(ctx) != "active" {
				if err := a.stopDaemon(ctx); err != nil {
					return err
				}
			}
			if err := a.systemctl(ctx, "daemon-reload"); err != nil {
				return err
			}
			if err := a.systemctl(ctx, "enable", "--now", unitName); err != nil {
				return err
			}
			a.done("%s bnmd installed and running as a user service (%s)", a.ui.dot("green"), unitName)
			return nil
		},
	}
	cmd.Flags().BoolVar(&unitOnly, "user-unit", false, "only write the unit file; do not run systemctl")
	return cmd
}

func unitFile(bin string) string {
	return fmt.Sprintf(`[Unit]
Description=bnm network daemon (NetworkManager front end)
Documentation=https://github.com/dopeCape/better-nm
After=network.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2
Slice=background.slice

[Install]
WantedBy=default.target
`, bin)
}

func (a *app) daemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the systemd user service",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			path := unitPath()
			if _, err := os.Stat(path); err != nil {
				a.done("no unit at %s; nothing to do", path)
				return nil
			}
			if haveSystemctl() {
				if err := a.systemctl(ctx, "disable", "--now", unitName); err != nil {
					fmt.Fprintf(a.errw, "warning: %v\n", err)
				}
			}
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove %s: %w", path, err)
			}
			if haveSystemctl() {
				if err := a.systemctl(ctx, "daemon-reload"); err != nil {
					fmt.Fprintf(a.errw, "warning: %v\n", err)
				}
			}
			a.done("removed %s", path)
			return nil
		},
	}
}

func (a *app) daemonLogsCmd() *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show bnmd's log (journal when it runs as a service, else the log file)",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if unitState(ctx) == "active" {
				if _, err := exec.LookPath("journalctl"); err == nil {
					jargs := []string{"--user", "-u", unitName, "-n", strconv.Itoa(lines), "--no-pager"}
					if follow {
						jargs = append(jargs, "-f")
					}
					j := exec.CommandContext(ctx, "journalctl", jargs...)
					j.Stdout, j.Stderr = a.out, a.errw
					if err := j.Run(); err != nil && ctx.Err() == nil {
						return fmt.Errorf("journalctl: %w", err)
					}
					return nil
				}
			}
			path := filepath.Join(paths.StateDir(), "bnmd.log")
			return tailFile(ctx, a.out, path, lines, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "how many recent lines")
	return cmd
}

// tailFile prints the last n lines of path and, with follow, keeps printing
// what gets appended until ctx ends.
func tailFile(ctx context.Context, w io.Writer, path string, n int, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		return core.Wrap(core.KindNotFound, "bnmd writes here when started by bnm; a systemd service logs to the journal", fmt.Errorf("open log: %w", err))
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if !follow {
		return nil
	}
	off := int64(len(data))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
		st, err := f.Stat()
		if err != nil {
			return nil
		}
		if st.Size() < off { // truncated / rotated
			off = 0
		}
		if st.Size() == off {
			continue
		}
		buf := make([]byte, st.Size()-off)
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil
		}
		off = st.Size()
		_, _ = w.Write(buf)
	}
}
