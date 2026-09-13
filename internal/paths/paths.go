// Package paths resolves the XDG locations bnm uses: the daemon socket, the
// config file and the state directory. Pure functions over the environment;
// tested by setting the variables in-process.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// RuntimeDir is the per-user directory holding the socket and lock file:
// $XDG_RUNTIME_DIR/bnm, falling back to /tmp/bnm-<uid>. BNM_RUNTIME_DIR overrides it.
func RuntimeDir() string {
	if v := os.Getenv("BNM_RUNTIME_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return filepath.Join(v, "bnm")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("bnm-%d", os.Getuid()))
}

// Socket is the daemon's Unix socket path. BNM_SOCKET overrides it.
func Socket() string {
	if v := os.Getenv("BNM_SOCKET"); v != "" {
		return v
	}
	return filepath.Join(RuntimeDir(), "bnmd.sock")
}

// ConfigDir is $XDG_CONFIG_HOME/bnm (default ~/.config/bnm).
func ConfigDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "bnm")
	}
	return filepath.Join(home(), ".config", "bnm")
}

// StateDir is $XDG_STATE_HOME/bnm (default ~/.local/state/bnm): logs and the history DB.
func StateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "bnm")
	}
	return filepath.Join(home(), ".local", "state", "bnm")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}
