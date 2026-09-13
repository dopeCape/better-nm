package client

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/dopeCape/better-nm/internal/paths"
)

// DaemonBinary is the executable New looks for.
const DaemonBinary = "bnmd"

// FindDaemon locates bnmd: BNM_DAEMON, then next to the running executable,
// then $PATH.
func FindDaemon() (string, error) {
	if v := os.Getenv("BNM_DAEMON"); v != "" {
		return v, nil
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		cand := filepath.Join(filepath.Dir(exe), DaemonBinary)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	p, err := exec.LookPath(DaemonBinary)
	if err != nil {
		return "", fmt.Errorf("client: %s not found next to %s or in PATH: %w", DaemonBinary, os.Args[0], err)
	}
	return p, nil
}

// spawn starts bnmd detached (own session, stdio to the log file).
func (c *Client) spawn() error {
	bin := c.daemonPath
	if bin == "" {
		var err error
		if bin, err = FindDaemon(); err != nil {
			return err
		}
	}
	logDir := paths.StateDir()
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("client: log dir: %w", err)
	}
	logf, err := os.OpenFile(filepath.Join(logDir, "bnmd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("client: open log: %w", err)
	}
	defer logf.Close()
	cmd := exec.Command(bin, "--socket", c.socket)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("client: start %s: %w", bin, err)
	}
	c.log.Debug("started bnmd", "path", bin, "pid", cmd.Process.Pid, "socket", c.socket)
	// Reap it if it exits while we live; it is not our child in any other sense.
	go func() { _ = cmd.Wait() }()
	return nil
}

// IsNotRunning reports whether err means the daemon is unreachable.
func IsNotRunning(err error) bool {
	var nr *ErrNotRunning
	return errors.As(err, &nr)
}
