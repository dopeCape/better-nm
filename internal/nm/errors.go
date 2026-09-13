package nm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// Sentinel errors. Every error the client returns wraps at most one of these;
// test with errors.Is. The NM message is always preserved in the chain.
var (
	// ErrPermissionDenied: polkit (or a profile ACL) refused. Err.Hint says what
	// one-time step fixes it.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrNotFound: no such profile / device / active connection / object.
	ErrNotFound = errors.New("not found")
	// ErrAuthFailed: the network rejected our credentials (wrong Wi-Fi password,
	// VPN login refused).
	ErrAuthFailed = errors.New("authentication failed")
	// ErrNoSecrets: NM needed a secret nobody could supply (no secret agent
	// registered; the profile marks the secret agent-owned or not-saved).
	ErrNoSecrets = errors.New("no secrets available")
	// ErrUnsupported: the operation or profile kind is not supported (by this NM,
	// by bnm v1, or by the mock in tests).
	ErrUnsupported = errors.New("unsupported")
	// ErrUnavailable: NetworkManager is not running / not on the bus.
	ErrUnavailable = errors.New("NetworkManager unavailable")
	// ErrConflict: the profile changed under us (Update2 version-id mismatch).
	ErrConflict = errors.New("profile changed concurrently")
	// ErrTimeout: an activation did not settle in time.
	ErrTimeout = errors.New("timed out")
)

// polkitHint is appended to permission errors. NM makes polkit checks with
// allow_interaction=TRUE, so with a session polkit agent the user gets a
// prompt; without one the call simply fails.
const polkitHint = "NetworkManager asked polkit and got no answer. Run a session polkit " +
	"authentication agent (polkit-gnome, polkit-kde-agent, lxpolkit, hyprpolkitagent, ...) " +
	"so prompts can be shown, or ask an admin to grant the action in a polkit rule. " +
	"Profiles bnm creates are scoped to your user and never prompt."

// Error is the typed error every D-Bus failure is wrapped in.
type Error struct {
	Op   string // what bnm was doing, e.g. "activate 8694ab34-..."
	Name string // D-Bus error name, e.g. org.freedesktop.NetworkManager.PermissionDenied
	Msg  string // NM's own message, verbatim
	Hint string // human hint (what one-time step fixes it), may be empty
	kind error  // one of the sentinels or nil
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("nm: ")
	b.WriteString(e.Op)
	b.WriteString(": ")
	switch {
	case e.kind != nil && e.Msg != "":
		fmt.Fprintf(&b, "%v: %s", e.kind, e.Msg)
	case e.kind != nil:
		b.WriteString(e.kind.Error())
	default:
		b.WriteString(e.Msg)
	}
	if e.Name != "" {
		fmt.Fprintf(&b, " (%s)", e.Name)
	}
	if e.Hint != "" {
		b.WriteString(". ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

// Unwrap exposes the sentinel for errors.Is.
func (e *Error) Unwrap() error { return e.kind }

// wrapDBus turns a godbus error into an *Error with the right sentinel. Non-D-Bus
// errors (context, I/O) are wrapped with the op only.
func wrapDBus(op string, err error) error {
	if err == nil {
		return nil
	}
	var de dbus.Error
	if !errors.As(err, &de) {
		var pde *dbus.Error
		if errors.As(err, &pde) {
			de = *pde
		} else {
			if errors.Is(err, dbus.ErrClosed) {
				return &Error{Op: op, Msg: err.Error(), kind: ErrUnavailable}
			}
			return fmt.Errorf("nm: %s: %w", op, err)
		}
	}
	e := &Error{Op: op, Name: de.Name, Msg: dbusMessage(de)}
	e.kind, e.Hint = classify(de.Name, e.Msg)
	return e
}

func dbusMessage(de dbus.Error) string {
	if len(de.Body) > 0 {
		if s, ok := de.Body[0].(string); ok {
			return s
		}
	}
	return de.Name
}

// classify maps a D-Bus error name onto a sentinel plus a hint.
func classify(name, msg string) (error, string) {
	suffix := name
	if i := strings.LastIndex(name, "."); i >= 0 {
		suffix = name[i+1:]
	}
	switch {
	case name == "org.freedesktop.DBus.Error.AccessDenied":
		return ErrPermissionDenied, polkitHint
	case name == "org.freedesktop.DBus.Error.ServiceUnknown",
		name == "org.freedesktop.DBus.Error.NameHasNoOwner",
		name == "org.freedesktop.DBus.Error.NoReply" && strings.Contains(msg, "disconnected"):
		return ErrUnavailable, "is NetworkManager running? (systemctl status NetworkManager)"
	case name == "org.freedesktop.DBus.Error.UnknownMethod",
		name == "org.freedesktop.DBus.Error.UnknownInterface",
		name == "org.freedesktop.DBus.Error.UnknownProperty":
		return ErrUnsupported, "this NetworkManager does not offer the call bnm needs (NM >= 1.44 is expected)"
	case name == "org.freedesktop.DBus.Error.UnknownObject":
		return ErrNotFound, ""
	case name == "org.freedesktop.DBus.Error.NoReply":
		return ErrTimeout, "NetworkManager did not answer in time; a polkit prompt may be waiting on your desktop"
	}
	if !strings.HasPrefix(name, "org.freedesktop.NetworkManager.") {
		return nil, ""
	}
	switch suffix {
	case "PermissionDenied":
		return ErrPermissionDenied, polkitHint
	case "UnknownConnection", "UnknownDevice", "SpecificObjectNotFound", "ConnectionNotActive",
		"SettingNotFound", "PropertyNotFound", "NotRegistered", "DoesNotExist":
		return ErrNotFound, ""
	case "NoSecrets", "UserCanceled", "AgentCanceled":
		return ErrNoSecrets, "no secret agent is registered for this session (bnm v1 does not register one); store the secret in the profile instead"
	case "NotSupported", "MissingPlugin", "NotSoftware", "MissingDependencies":
		return ErrUnsupported, ""
	case "VersionIdMismatch":
		return ErrConflict, "re-read the profile and retry"
	case "LoginFailed":
		return ErrAuthFailed, ""
	}
	return nil, ""
}

// newErr builds an *Error that does not come from D-Bus.
func newErr(op string, kind error, msg string) error {
	e := &Error{Op: op, Msg: msg, kind: kind}
	if errors.Is(kind, ErrPermissionDenied) {
		e.Hint = polkitHint
	}
	return e
}

// PolkitAuthActions lists the NM polkit actions GetPermissions reports as "auth":
// each of those will only work when a session polkit agent can show a prompt.
// Surfaces use it to warn the user once instead of failing later.
func PolkitAuthActions(perms map[string]string) []string {
	var out []string
	for k, v := range perms {
		if v == "auth" {
			out = append(out, k)
		}
	}
	return out
}
