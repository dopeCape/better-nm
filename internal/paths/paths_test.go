package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPaths(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	tests := []struct {
		name string
		env  map[string]string
		fn   func() string
		want string
	}{
		{"runtime xdg", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1"}, RuntimeDir, "/run/user/1/bnm"},
		{"runtime override", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1", "BNM_RUNTIME_DIR": "/x"}, RuntimeDir, "/x"},
		{"runtime fallback", map[string]string{"XDG_RUNTIME_DIR": ""}, RuntimeDir, filepath.Join(os.TempDir(), fmt.Sprintf("bnm-%d", os.Getuid()))},
		{"socket", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1"}, Socket, "/run/user/1/bnm/bnmd.sock"},
		{"socket override", map[string]string{"BNM_SOCKET": "/s"}, Socket, "/s"},
		{"config xdg", map[string]string{"XDG_CONFIG_HOME": "/c"}, ConfigDir, "/c/bnm"},
		{"config home", map[string]string{"XDG_CONFIG_HOME": ""}, ConfigDir, "/home/u/.config/bnm"},
		{"state xdg", map[string]string{"XDG_STATE_HOME": "/s"}, StateDir, "/s/bnm"},
		{"state home", map[string]string{"XDG_STATE_HOME": ""}, StateDir, "/home/u/.local/state/bnm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"XDG_RUNTIME_DIR", "BNM_RUNTIME_DIR", "BNM_SOCKET", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := tt.fn(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
