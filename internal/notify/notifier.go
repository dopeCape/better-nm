package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/dopeCape/better-nm/internal/core"
)

const (
	appName        = "bnm"
	expireTimeout  = 8 * time.Second
	deliverTimeout = 10 * time.Second
	busName        = "org.freedesktop.Notifications"
	busPath        = dbus.ObjectPath("/org/freedesktop/Notifications")
	notifyMethod   = busName + ".Notify"
)

// Notifier implements core.Notifier: Policy filtering in front of delivery
// over org.freedesktop.Notifications, with notify-send as the fallback.
type Notifier struct {
	filter *Filter
	log    *slog.Logger

	// busAddress overrides the session bus address (tests); "" reads
	// DBUS_SESSION_BUS_ADDRESS without auto-launching a bus.
	busAddress string
	// notifySend is the fallback binary looked up on PATH.
	notifySend string
}

// New returns a Notifier that applies policy and delivers what passes. Held
// (debounced) events are delivered from a timer goroutine with their own
// timeout; failures there are logged.
func New(policy Policy, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	n := &Notifier{
		filter:     NewFilter(policy),
		log:        logger,
		notifySend: "notify-send",
	}
	n.filter.OnReady(func(e core.Event) {
		ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
		defer cancel()
		if err := n.Deliver(ctx, e); err != nil {
			n.log.Warn("notify: held event not delivered", "type", e.Type, "network", e.NetworkKey, "err", err)
		}
	})
	return n
}

// Filter exposes the policy filter (for Flush at shutdown and inspection).
func (n *Notifier) Filter() *Filter { return n.filter }

// Notify applies the policy and delivers e if it passes. Filtered events
// return nil; a connected event may be delivered later by the debounce timer.
func (n *Notifier) Notify(ctx context.Context, e core.Event) error {
	if !n.filter.Allow(e) {
		n.log.Debug("notify: filtered", "type", e.Type, "network", e.NetworkKey)
		return nil
	}
	return n.Deliver(ctx, e)
}

// Deliver sends e to the desktop, bypassing the policy (bnm notify test).
// It tries the session bus first, then notify-send; if both fail the error
// is logged and returned.
func (n *Notifier) Deliver(ctx context.Context, e core.Event) error {
	busErr := n.deliverBus(ctx, e)
	if busErr == nil {
		return nil
	}
	n.log.Debug("notify: session bus failed, trying notify-send", "err", busErr)
	sendErr := n.deliverNotifySend(ctx, e)
	if sendErr == nil {
		return nil
	}
	err := fmt.Errorf("notify: deliver %q: bus: %w; notify-send: %w", e.Title, busErr, sendErr)
	n.log.Error("notify: delivery failed", "type", e.Type, "err", err)
	return err
}

func (n *Notifier) connect() (*dbus.Conn, error) {
	var conn *dbus.Conn
	var err error
	if n.busAddress != "" {
		conn, err = dbus.Dial(n.busAddress)
	} else {
		conn, err = dbus.SessionBusPrivateNoAutoStartup()
	}
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("auth: %w", err)
	}
	if err := conn.Hello(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	return conn, nil
}

func (n *Notifier) deliverBus(ctx context.Context, e core.Event) error {
	conn, err := n.connect()
	if err != nil {
		return err
	}
	defer conn.Close()
	hints := map[string]dbus.Variant{
		"urgency":       dbus.MakeVariant(urgencyLevel(e.Urgency)),
		"desktop-entry": dbus.MakeVariant(appName),
	}
	call := conn.Object(busName, busPath).CallWithContext(ctx, notifyMethod, 0,
		appName, uint32(0), iconFor(e.Type), e.Title, e.Body, []string{}, hints, int32(expireTimeout/time.Millisecond))
	if call.Err != nil {
		return call.Err
	}
	return nil
}

func (n *Notifier) deliverNotifySend(ctx context.Context, e core.Event) error {
	path, err := exec.LookPath(n.notifySend)
	if err != nil {
		return err
	}
	args := []string{
		"-a", appName,
		"-u", urgencyName(e.Urgency),
		"-i", iconFor(e.Type),
		"-t", strconv.Itoa(int(expireTimeout / time.Millisecond)),
		e.Title,
	}
	if e.Body != "" {
		args = append(args, e.Body)
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("%w: %s", err, out)
		}
		return err
	}
	return nil
}

// iconFor picks the freedesktop icon name for an event type.
func iconFor(t core.EventType) string {
	switch t {
	case core.EventVPNUp, core.EventVPNDown:
		return "network-vpn"
	case core.EventNoInternet, core.EventDegraded:
		return "dialog-warning"
	default:
		return "network-wireless"
	}
}

// urgencyLevel maps the Event urgency to the Notifications spec byte.
func urgencyLevel(u string) byte {
	switch u {
	case "low":
		return 0
	case "critical":
		return 2
	default:
		return 1
	}
}

func urgencyName(u string) string {
	switch u {
	case "low", "critical":
		return u
	default:
		return "normal"
	}
}

var _ core.Notifier = (*Notifier)(nil)
