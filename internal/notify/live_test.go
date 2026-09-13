//go:build live

package notify

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// TestLiveNotification sends one real desktop notification through the
// user's session bus (what `bnm notify test` does).
func TestLiveNotification(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("no session bus")
	}
	n := New(DefaultPolicy(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := n.Deliver(ctx, core.Event{
		Time:       time.Now(),
		Type:       core.EventConnected,
		NetworkKey: "wifi:test",
		Title:      "bnm test notification",
		Body:       "Delivered by internal/notify live test",
		Urgency:    "low",
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	t.Log("notification delivered without error")
}
