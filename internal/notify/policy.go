// Package notify owns desktop notifications: the Policy that says which
// Events are notable (per-type switches, muted Network Keys, a debounce that
// cancels connect/disconnect flaps and a per-type per-network rate limit) and
// the Notifier that delivers them over org.freedesktop.Notifications on the
// session bus, falling back to notify-send.
//
// The policy is fixed by issue #25. Tested with an injectable clock for the
// Filter, a fake org.freedesktop.Notifications exported on a private
// dbus-daemon for delivery (skipped when dbus-daemon is missing), and a fake
// notify-send on PATH for the fallback.
package notify

import (
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Policy decides which events reach the desktop.
type Policy struct {
	// Enabled switches each event type; types absent from the map are off.
	Enabled map[core.EventType]bool
	// MutedNetworks are Network Keys whose events are dropped.
	MutedNetworks []string
	// Debounce is the window in which a connected/disconnected pair for the
	// same Network Key cancels itself out (Wi-Fi roaming and reconnects).
	Debounce time.Duration
	// RateLimit is the minimum gap between two notifications of the same
	// type for the same Network Key.
	RateLimit time.Duration
}

// DefaultPolicy is issue #25's table: connected, disconnected, no-internet,
// internet-restored, vpn-up and vpn-down on; degraded and recovered off;
// 5 s debounce; 30 s rate limit. secret-needed (NetworkManager waiting for a
// password through bnm's agent) is on and bypasses debounce, rate limit and
// mutes: every prompt must reach the user.
func DefaultPolicy() Policy {
	return Policy{
		Enabled: map[core.EventType]bool{
			core.EventConnected:        true,
			core.EventDisconnected:     true,
			core.EventNoInternet:       true,
			core.EventInternetRestored: true,
			core.EventVPNUp:            true,
			core.EventVPNDown:          true,
			core.EventDegraded:         false,
			core.EventRecovered:        false,
			core.EventSecretNeeded:     true,
		},
		Debounce:  5 * time.Second,
		RateLimit: 30 * time.Second,
	}
}

// IsEnabled reports whether events of type t are notable under this policy.
func (p Policy) IsEnabled(t core.EventType) bool {
	return p.Enabled[t]
}

// IsMuted reports whether key is on the mute list.
func (p Policy) IsMuted(key string) bool {
	if key == "" {
		return false
	}
	for _, m := range p.MutedNetworks {
		if m == key {
			return true
		}
	}
	return false
}
