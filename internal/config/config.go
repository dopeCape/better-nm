// Package config owns bnm's TOML settings file at $XDG_CONFIG_HOME/bnm/config.toml:
// the typed Config struct with its defaults, loading and saving, and dotted-key
// access (`notify.degraded`) for `bnm config get|set`. Tested in-process against a
// temp directory; nothing here touches the network.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/paths"
)

// FileName is the config file's base name inside the config directory.
const FileName = "config.toml"

// Config is the whole settings file. Field tags double as the dotted key names.
type Config struct {
	Monitor   Monitor   `toml:"monitor" json:"monitor"`
	Notify    Notify    `toml:"notify" json:"notify"`
	Speed     Speed     `toml:"speed" json:"speed"`
	Tailscale Tailscale `toml:"tailscale" json:"tailscale"`
	Daemon    Daemon    `toml:"daemon" json:"daemon"`
}

// Monitor tunes the probe engine.
type Monitor struct {
	Interval      time.Duration `toml:"interval" json:"interval"`
	Anchors       []string      `toml:"anchors" json:"anchors"`
	RetentionDays int           `toml:"retention_days" json:"retention_days"`
}

// Notify is the notification policy: which event types are delivered, and
// which Network Keys are muted.
type Notify struct {
	Connected        bool     `toml:"connected" json:"connected"`
	Disconnected     bool     `toml:"disconnected" json:"disconnected"`
	NoInternet       bool     `toml:"no_internet" json:"no_internet"`
	InternetRestored bool     `toml:"internet_restored" json:"internet_restored"`
	VPNUp            bool     `toml:"vpn_up" json:"vpn_up"`
	VPNDown          bool     `toml:"vpn_down" json:"vpn_down"`
	Degraded         bool     `toml:"degraded" json:"degraded"`
	Recovered        bool     `toml:"recovered" json:"recovered"`
	MutedNetworks    []string `toml:"muted_networks" json:"muted_networks"`
}

// Speed selects the default speed-test provider.
type Speed struct {
	Provider     string `toml:"provider" json:"provider"`
	Iperf3Server string `toml:"iperf3_server" json:"iperf3_server"`
	MaxBytes     int64  `toml:"max_bytes" json:"max_bytes"`
}

// Tailscale locates tailscaled.
type Tailscale struct {
	Socket string `toml:"socket" json:"socket"`
}

// Daemon tunes bnmd itself.
type Daemon struct {
	LogLevel string `toml:"log_level" json:"log_level"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Monitor: Monitor{
			Interval:      30 * time.Second,
			Anchors:       []string{"1.1.1.1", "8.8.8.8"},
			RetentionDays: 30,
		},
		Notify: Notify{
			Connected:        true,
			Disconnected:     true,
			NoInternet:       true,
			InternetRestored: true,
			VPNUp:            true,
			VPNDown:          true,
			Degraded:         false,
			Recovered:        false,
			MutedNetworks:    []string{},
		},
		Speed: Speed{
			Provider:     "cloudflare",
			Iperf3Server: "",
			MaxBytes:     300_000_000,
		},
		Tailscale: Tailscale{Socket: ""},
		Daemon:    Daemon{LogLevel: "info"},
	}
}

// Path is where Load and Save read and write: $XDG_CONFIG_HOME/bnm/config.toml.
func Path() string {
	return filepath.Join(paths.ConfigDir(), FileName)
}

// Load reads Path(). A missing file yields the defaults and no error; keys
// absent from the file keep their default.
func Load() (Config, error) {
	return LoadFrom(Path())
}

// LoadFrom is Load for an explicit path.
func LoadFrom(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return c, fmt.Errorf("config: read %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return Default(), fmt.Errorf("config: parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		// Unknown keys are tolerated (forward compatibility) but reported.
		return c, &UnknownKeysError{Keys: keys}
	}
	return c, nil
}

// UnknownKeysError reports keys in the file that Config does not know. The
// returned Config is still fully usable.
type UnknownKeysError struct{ Keys []string }

func (e *UnknownKeysError) Error() string {
	return "config: unknown keys: " + strings.Join(e.Keys, ", ")
}

// Save writes c to Path(), creating the directory (0700) as needed.
func (c Config) Save() error {
	return c.SaveTo(Path())
}

// SaveTo is Save for an explicit path. The write is atomic (temp file + rename).
func (c Config) SaveTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("config: write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("config: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

// Keys lists every settable dotted key, sorted.
func Keys() []string {
	var keys []string
	walk(reflect.ValueOf(Config{}), "", func(key string, _ reflect.Value) { keys = append(keys, key) })
	sort.Strings(keys)
	return keys
}

// Get returns the value at a dotted key formatted as a string: bools as
// true/false, durations as "30s", lists comma-joined.
func (c Config) Get(key string) (string, error) {
	v, err := lookup(reflect.ValueOf(&c).Elem(), key)
	if err != nil {
		return "", err
	}
	return format(v), nil
}

// Set parses value for the field at key and stores it. Lists take a comma
// separated string ("" = empty list); durations take Go syntax ("30s", "2m").
func (c *Config) Set(key, value string) error {
	v, err := lookup(reflect.ValueOf(c).Elem(), key)
	if err != nil {
		return err
	}
	if err := parseInto(v, strings.TrimSpace(value)); err != nil {
		return core.Errorf(core.KindInvalid, "", "config: %s: %v", key, err)
	}
	return c.validate(key)
}

// Mute adds a Network Key to notify.muted_networks (idempotent).
func (c *Config) Mute(networkKey string) {
	for _, k := range c.Notify.MutedNetworks {
		if k == networkKey {
			return
		}
	}
	c.Notify.MutedNetworks = append(c.Notify.MutedNetworks, networkKey)
}

// Unmute removes a Network Key from notify.muted_networks; reports whether it was there.
func (c *Config) Unmute(networkKey string) bool {
	for i, k := range c.Notify.MutedNetworks {
		if k == networkKey {
			c.Notify.MutedNetworks = append(c.Notify.MutedNetworks[:i], c.Notify.MutedNetworks[i+1:]...)
			return true
		}
	}
	return false
}

// Enabled says whether the policy delivers an event of this type. Types the
// policy does not know (wifi-scan, state-changed) are never delivered.
func (n Notify) Enabled(t core.EventType) bool {
	switch t {
	case core.EventConnected:
		return n.Connected
	case core.EventDisconnected:
		return n.Disconnected
	case core.EventNoInternet:
		return n.NoInternet
	case core.EventInternetRestored:
		return n.InternetRestored
	case core.EventVPNUp:
		return n.VPNUp
	case core.EventVPNDown:
		return n.VPNDown
	case core.EventDegraded:
		return n.Degraded
	case core.EventRecovered:
		return n.Recovered
	}
	return false
}

// Muted says whether a Network Key is muted.
func (n Notify) Muted(networkKey string) bool {
	if networkKey == "" {
		return false
	}
	for _, k := range n.MutedNetworks {
		if k == networkKey {
			return true
		}
	}
	return false
}

func (c *Config) validate(key string) error {
	switch key {
	case "monitor.interval":
		if c.Monitor.Interval < time.Second {
			return core.Errorf(core.KindInvalid, "", "config: monitor.interval must be at least 1s")
		}
	case "monitor.retention_days":
		if c.Monitor.RetentionDays < 1 {
			return core.Errorf(core.KindInvalid, "", "config: monitor.retention_days must be at least 1")
		}
	case "speed.provider":
		switch c.Speed.Provider {
		case "cloudflare", "iperf3", "librespeed":
		default:
			return core.Errorf(core.KindInvalid, "", "config: speed.provider must be cloudflare, iperf3 or librespeed")
		}
	case "speed.max_bytes":
		if c.Speed.MaxBytes <= 0 {
			return core.Errorf(core.KindInvalid, "", "config: speed.max_bytes must be positive")
		}
	case "daemon.log_level":
		switch c.Daemon.LogLevel {
		case "debug", "info", "warn", "error":
		default:
			return core.Errorf(core.KindInvalid, "", "config: daemon.log_level must be debug, info, warn or error")
		}
	}
	return nil
}

// walk visits every leaf field with its dotted key.
func walk(v reflect.Value, prefix string, fn func(key string, v reflect.Value)) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("toml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct && fv.Type() != reflect.TypeOf(time.Duration(0)) {
			walk(fv, key, fn)
			continue
		}
		fn(key, fv)
	}
}

func lookup(root reflect.Value, key string) (reflect.Value, error) {
	var found reflect.Value
	walk(root, "", func(k string, v reflect.Value) {
		if k == key {
			found = v
		}
	})
	if !found.IsValid() {
		return reflect.Value{}, core.Errorf(core.KindNotFound, "run `bnm config keys` to list them", "config: unknown key %q", key)
	}
	return found, nil
}

func format(v reflect.Value) string {
	switch v.Interface().(type) {
	case time.Duration:
		return v.Interface().(time.Duration).String()
	case []string:
		return strings.Join(v.Interface().([]string), ",")
	}
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.String:
		return v.String()
	}
	return fmt.Sprint(v.Interface())
}

func parseInto(v reflect.Value, s string) error {
	switch v.Interface().(type) {
	case time.Duration:
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("want a duration like 30s: %w", err)
		}
		v.Set(reflect.ValueOf(d))
		return nil
	case []string:
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if out == nil {
			out = []string{}
		}
		v.Set(reflect.ValueOf(out))
		return nil
	}
	switch v.Kind() {
	case reflect.Bool:
		b, err := parseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("want an integer: %w", err)
		}
		v.SetInt(n)
	case reflect.String:
		v.SetString(s)
	default:
		return fmt.Errorf("unsupported field type %s", v.Type())
	}
	return nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "on", "yes", "1":
		return true, nil
	case "false", "off", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("want true or false, got %q", s)
}
