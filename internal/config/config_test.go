package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.Monitor.Interval != 30*time.Second {
		t.Errorf("interval = %v", c.Monitor.Interval)
	}
	if got := strings.Join(c.Monitor.Anchors, ","); got != "1.1.1.1,8.8.8.8" {
		t.Errorf("anchors = %q", got)
	}
	if c.Monitor.RetentionDays != 30 || c.Speed.Provider != "cloudflare" || c.Speed.MaxBytes != 300_000_000 || c.Daemon.LogLevel != "info" {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if !c.Notify.Connected || !c.Notify.Disconnected || !c.Notify.NoInternet || !c.Notify.InternetRestored || !c.Notify.VPNUp || !c.Notify.VPNDown {
		t.Errorf("notify defaults should be on: %+v", c.Notify)
	}
	if c.Notify.Degraded || c.Notify.Recovered {
		t.Errorf("degraded/recovered should default off")
	}
}

func TestPathUsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	if got, want := Path(), "/x/cfg/bnm/config.toml"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissingIsDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Monitor.Interval != 30*time.Second || !c.Notify.Connected || c.Speed.Provider != "cloudflare" {
		t.Errorf("Load() on missing file should be Default, got %+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	c := Default()
	c.Monitor.Interval = 45 * time.Second
	c.Monitor.Anchors = []string{"9.9.9.9"}
	c.Notify.Degraded = true
	c.Notify.MutedNetworks = []string{"wifi:Cafe"}
	c.Speed.Provider = "iperf3"
	c.Speed.Iperf3Server = "10.0.0.2:5201"
	c.Tailscale.Socket = "/run/ts.sock"
	c.Daemon.LogLevel = "debug"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, "bnm", "config.toml")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v mode %v", err, st.Mode())
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Monitor.Interval != 45*time.Second || got.Monitor.Anchors[0] != "9.9.9.9" || !got.Notify.Degraded ||
		got.Notify.MutedNetworks[0] != "wifi:Cafe" || got.Speed.Provider != "iperf3" || got.Speed.Iperf3Server != "10.0.0.2:5201" ||
		got.Tailscale.Socket != "/run/ts.sock" || got.Daemon.LogLevel != "debug" {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("[notify]\ndegraded = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFrom(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Notify.Degraded || !c.Notify.Connected || c.Monitor.Interval != 30*time.Second {
		t.Errorf("partial load: %+v", c)
	}
}

func TestLoadUnknownKeysReported(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[notify]\nbogus = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFrom(p)
	var uk *UnknownKeysError
	if !errors.As(err, &uk) || uk.Keys[0] != "notify.bogus" {
		t.Fatalf("want UnknownKeysError, got %v", err)
	}
	if !c.Notify.Connected {
		t.Errorf("config should still be usable")
	}
}

func TestLoadBadTOML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[notify\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(p); err == nil {
		t.Fatal("want parse error")
	}
}

func TestSetGet(t *testing.T) {
	tests := []struct {
		key, value, want string
		wantErr          bool
	}{
		{"notify.degraded", "true", "true", false},
		{"notify.degraded", "off", "false", false},
		{"notify.connected", "maybe", "", true},
		{"monitor.interval", "1m", "1m0s", false},
		{"monitor.interval", "500ms", "", true},
		{"monitor.interval", "soon", "", true},
		{"monitor.anchors", "9.9.9.9, 1.0.0.1", "9.9.9.9,1.0.0.1", false},
		{"monitor.anchors", "", "", false},
		{"monitor.retention_days", "7", "7", false},
		{"monitor.retention_days", "0", "", true},
		{"monitor.retention_days", "x", "", true},
		{"speed.provider", "iperf3", "iperf3", false},
		{"speed.provider", "fast.com", "", true},
		{"speed.max_bytes", "1000", "1000", false},
		{"speed.max_bytes", "-1", "", true},
		{"speed.iperf3_server", "h:5201", "h:5201", false},
		{"tailscale.socket", "/run/x", "/run/x", false},
		{"daemon.log_level", "debug", "debug", false},
		{"daemon.log_level", "loud", "", true},
		{"notify.muted_networks", "wifi:A,wifi:B", "wifi:A,wifi:B", false},
		{"nope.key", "1", "", true},
		{"notify", "1", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			c := Default()
			err := c.Set(tt.key, tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error")
				}
				if k := core.KindOf(err); k != core.KindInvalid && k != core.KindNotFound {
					t.Errorf("error kind = %s", k)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Get(tt.key)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Get = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetUnknown(t *testing.T) {
	c := Default()
	if _, err := c.Get("monitor.nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("want not found, got %v", err)
	}
}

func TestKeys(t *testing.T) {
	keys := Keys()
	want := []string{"daemon.log_level", "monitor.anchors", "monitor.interval", "monitor.retention_days",
		"notify.connected", "notify.degraded", "notify.disconnected", "notify.internet_restored", "notify.muted_networks",
		"notify.no_internet", "notify.recovered", "notify.vpn_down", "notify.vpn_up",
		"speed.iperf3_server", "speed.max_bytes", "speed.provider", "tailscale.socket"}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Errorf("Keys() = %v", keys)
	}
}

func TestMuteUnmuteEnabled(t *testing.T) {
	c := Default()
	c.Mute("wifi:A")
	c.Mute("wifi:A")
	if len(c.Notify.MutedNetworks) != 1 || !c.Notify.Muted("wifi:A") || c.Notify.Muted("wifi:B") || c.Notify.Muted("") {
		t.Errorf("mute: %+v", c.Notify.MutedNetworks)
	}
	if !c.Unmute("wifi:A") || c.Unmute("wifi:A") || len(c.Notify.MutedNetworks) != 0 {
		t.Errorf("unmute failed")
	}
	n := c.Notify
	if !n.Enabled(core.EventConnected) || n.Enabled(core.EventDegraded) || n.Enabled(core.EventWifiScan) || n.Enabled(core.EventStateChanged) {
		t.Errorf("Enabled mapping wrong")
	}
}
