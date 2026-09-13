package notify

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/dopeCape/better-nm/internal/core"
)

// startBus forks a private dbus-daemon and returns its address, or skips.
func startBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not installed")
	}
	out, err := exec.Command("dbus-daemon", "--session", "--print-address", "--print-pid", "--fork").Output()
	if err != nil {
		t.Skipf("dbus-daemon failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Skipf("unexpected dbus-daemon output %q", out)
	}
	addr := strings.TrimSpace(lines[0])
	pid, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	if err != nil {
		t.Skipf("bad pid %q", lines[1])
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	return addr
}

type notifyCall struct {
	app     string
	replace uint32
	icon    string
	summary string
	body    string
	actions []string
	hints   map[string]dbus.Variant
	timeout int32
}

// fakeServer is a minimal org.freedesktop.Notifications.
type fakeServer struct {
	mu    sync.Mutex
	calls []notifyCall
	fail  bool
}

func (s *fakeServer) Notify(app string, replace uint32, icon, summary, body string, actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return 0, dbus.NewError("org.freedesktop.Notifications.Error.Test", nil)
	}
	s.calls = append(s.calls, notifyCall{app, replace, icon, summary, body, actions, hints, timeout})
	return uint32(len(s.calls)), nil
}

func (s *fakeServer) last(t *testing.T) notifyCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("no Notify call recorded")
	}
	return s.calls[len(s.calls)-1]
}

func serveFake(t *testing.T, addr string) *fakeServer {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("connect to private bus: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	srv := &fakeServer{}
	if err := conn.Export(srv, busPath, busName); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v (%v)", err, reply)
	}
	return srv
}

// fakeNotifySend puts a script named notify-send first on PATH that appends
// its arguments to a log file, and returns that file's path.
func fakeNotifySend(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$BNM_TEST_NOTIFY_LOG\"\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BNM_TEST_NOTIFY_LOG", log)
	return log
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestDeliverOverBus(t *testing.T) {
	addr := startBus(t)
	srv := serveFake(t, addr)
	n := New(DefaultPolicy(), quietLogger())
	n.busAddress = addr
	n.notifySend = "definitely-not-installed-notify-send"

	e := core.Event{
		Type:       core.EventVPNUp,
		NetworkKey: "tailscale:x",
		Title:      "VPN up",
		Body:       "Tailscale connected",
		Urgency:    "low",
	}
	if err := n.Notify(context.Background(), e); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	c := srv.last(t)
	if c.app != "bnm" || c.replace != 0 || c.icon != "network-vpn" || c.summary != "VPN up" || c.body != "Tailscale connected" {
		t.Errorf("call = %+v", c)
	}
	if len(c.actions) != 0 {
		t.Errorf("actions = %v", c.actions)
	}
	if c.timeout != 8000 {
		t.Errorf("timeout = %d", c.timeout)
	}
	if u, ok := c.hints["urgency"].Value().(byte); !ok || u != 0 {
		t.Errorf("urgency hint = %v", c.hints["urgency"])
	}

	// Notify goes through the filter: vpn-up on the same key is rate-limited
	// now, but a critical no-internet passes with the warning icon.
	if err := n.Notify(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	count := len(srv.calls)
	srv.mu.Unlock()
	if count != 1 {
		t.Errorf("rate-limited event was delivered (%d calls)", count)
	}
	if err := n.Notify(context.Background(), core.Event{Type: core.EventNoInternet, NetworkKey: "wifi:Home", Title: "No internet", Urgency: "critical"}); err != nil {
		t.Fatal(err)
	}
	c = srv.last(t)
	if c.icon != "dialog-warning" || c.hints["urgency"].Value().(byte) != 2 {
		t.Errorf("no-internet call = %+v", c)
	}
	// Default urgency is normal (1).
	_ = n.Deliver(context.Background(), core.Event{Type: core.EventConnected, Title: "Connected"})
	c = srv.last(t)
	if c.icon != "network-wireless" || c.hints["urgency"].Value().(byte) != 1 {
		t.Errorf("connected call = %+v", c)
	}
}

func TestDeliverFallsBackToNotifySend(t *testing.T) {
	log := fakeNotifySend(t, 0)
	n := New(DefaultPolicy(), quietLogger())
	n.busAddress = "unix:path=" + filepath.Join(t.TempDir(), "no-such-bus")

	err := n.Deliver(context.Background(), core.Event{Type: core.EventDisconnected, Title: "Disconnected", Body: "Home", Urgency: "normal"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("fake notify-send was not called: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"-a", "bnm", "-u", "normal", "-i", "network-wireless", "-t", "8000", "Disconnected", "Home"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("notify-send args = %v, want %v", got, want)
	}
}

func TestDeliverFallbackWhenBusMethodFails(t *testing.T) {
	addr := startBus(t)
	srv := serveFake(t, addr)
	srv.mu.Lock()
	srv.fail = true
	srv.mu.Unlock()
	log := fakeNotifySend(t, 0)
	n := New(DefaultPolicy(), quietLogger())
	n.busAddress = addr
	if err := n.Deliver(context.Background(), core.Event{Type: core.EventVPNDown, Title: "VPN down"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if _, err := os.Stat(log); err != nil {
		t.Fatal("notify-send fallback not used after a bus error")
	}
}

func TestDeliverBothFail(t *testing.T) {
	fakeNotifySend(t, 3)
	n := New(DefaultPolicy(), quietLogger())
	n.busAddress = "unix:path=" + filepath.Join(t.TempDir(), "no-such-bus")
	err := n.Deliver(context.Background(), core.Event{Type: core.EventConnected, Title: "x"})
	if err == nil {
		t.Fatal("expected an error when both paths fail")
	}
	if !strings.Contains(err.Error(), "bus:") || !strings.Contains(err.Error(), "notify-send:") {
		t.Errorf("error should name both paths: %v", err)
	}
}

func TestNotifyFiltered(t *testing.T) {
	fakeNotifySend(t, 3)
	n := New(DefaultPolicy(), quietLogger())
	n.busAddress = "unix:path=" + filepath.Join(t.TempDir(), "no-such-bus")
	// Disabled type: filtered before any delivery attempt, so no error.
	if err := n.Notify(context.Background(), core.Event{Type: core.EventDegraded, Title: "slow"}); err != nil {
		t.Fatalf("filtered event returned %v", err)
	}
	// Held connected: nothing delivered yet.
	if err := n.Notify(context.Background(), core.Event{Type: core.EventConnected, NetworkKey: "wifi:Home", Title: "c"}); err != nil {
		t.Fatalf("held event returned %v", err)
	}
	if n.Filter().Pending() != 1 {
		t.Fatal("connected not held")
	}
	// Flush delivers through OnReady (fails here, logged only).
	n.Filter().Flush()
	if n.Filter().Pending() != 0 {
		t.Fatal("not flushed")
	}
}

func TestHeldEventDeliveredByTimer(t *testing.T) {
	log := fakeNotifySend(t, 0)
	p := DefaultPolicy()
	p.Debounce = 50 * time.Millisecond
	n := New(p, quietLogger())
	n.busAddress = "unix:path=" + filepath.Join(t.TempDir(), "no-such-bus")
	if err := n.Notify(context.Background(), core.Event{Type: core.EventConnected, NetworkKey: "wifi:Home", Title: "Connected"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(log); err == nil && strings.Contains(string(b), "Connected") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("held connected event was not delivered by the timer")
}

func TestIconsAndUrgency(t *testing.T) {
	icons := map[core.EventType]string{
		core.EventConnected: "network-wireless", core.EventDisconnected: "network-wireless",
		core.EventInternetRestored: "network-wireless", core.EventRecovered: "network-wireless",
		core.EventNoInternet: "dialog-warning", core.EventDegraded: "dialog-warning",
		core.EventVPNUp: "network-vpn", core.EventVPNDown: "network-vpn",
	}
	for ty, want := range icons {
		if got := iconFor(ty); got != want {
			t.Errorf("iconFor(%s) = %s, want %s", ty, got, want)
		}
	}
	for in, want := range map[string]byte{"low": 0, "": 1, "normal": 1, "weird": 1, "critical": 2} {
		if got := urgencyLevel(in); got != want {
			t.Errorf("urgencyLevel(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]string{"low": "low", "": "normal", "x": "normal", "critical": "critical"} {
		if got := urgencyName(in); got != want {
			t.Errorf("urgencyName(%q) = %s, want %s", in, got, want)
		}
	}
}
