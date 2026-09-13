package nm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestWrapDBus(t *testing.T) {
	tests := []struct {
		name     string
		dbusName string
		msg      string
		want     error // sentinel or nil
		hint     bool
	}{
		{"nm permission denied", "org.freedesktop.NetworkManager.PermissionDenied", "Not authorized to control networking.", ErrPermissionDenied, true},
		{"settings permission denied", "org.freedesktop.NetworkManager.Settings.PermissionDenied", "Insufficient privileges.", ErrPermissionDenied, true},
		{"dbus access denied", "org.freedesktop.DBus.Error.AccessDenied", "Rejected send message", ErrPermissionDenied, true},
		{"unknown connection", "org.freedesktop.NetworkManager.UnknownConnection", "Connection was not provided by any settings service", ErrNotFound, false},
		{"unknown device", "org.freedesktop.NetworkManager.UnknownDevice", "Device not found", ErrNotFound, false},
		{"not active", "org.freedesktop.NetworkManager.ConnectionNotActive", "The connection was not active.", ErrNotFound, false},
		{"unknown object", "org.freedesktop.DBus.Error.UnknownObject", "No such object path", ErrNotFound, false},
		{"mock does not exist", "org.freedesktop.NetworkManager.DoesNotExist", "Access point with SSID [x] could not be found", ErrNotFound, false},
		{"no secrets", "org.freedesktop.NetworkManager.AgentManager.NoSecrets", "No agents were available for this request.", ErrNoSecrets, true},
		{"user canceled", "org.freedesktop.NetworkManager.SecretAgent.UserCanceled", "canceled", ErrNoSecrets, true},
		{"not supported", "org.freedesktop.NetworkManager.Settings.NotSupported", "unsupported", ErrUnsupported, false},
		{"missing plugin", "org.freedesktop.NetworkManager.MissingPlugin", "no VPN plugin", ErrUnsupported, false},
		{"unknown method", "org.freedesktop.DBus.Error.UnknownMethod", "No such method 'Update2'", ErrUnsupported, true},
		{"service unknown", "org.freedesktop.DBus.Error.ServiceUnknown", "The name org.freedesktop.NetworkManager was not provided by any .service files", ErrUnavailable, true},
		{"version mismatch", "org.freedesktop.NetworkManager.Settings.VersionIdMismatch", "The profile changed since the last read", ErrConflict, true},
		{"no reply", "org.freedesktop.DBus.Error.NoReply", "Message recipient disconnected from message bus without replying", ErrUnavailable, true},
		{"timeout", "org.freedesktop.DBus.Error.NoReply", "Did not receive a reply.", ErrTimeout, true},
		{"unclassified nm error", "org.freedesktop.NetworkManager.Device.IncompatibleConnection", "not compatible", nil, false},
		{"foreign error", "com.example.Whatever", "boom", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := dbus.Error{Name: tt.dbusName, Body: []any{tt.msg}}
			err := wrapDBus("test op", in)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("not an *Error: %T %v", err, err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("errors.Is(%v) false: %v", tt.want, err)
			}
			if tt.want == nil {
				for _, s := range []error{ErrPermissionDenied, ErrNotFound, ErrAuthFailed, ErrNoSecrets, ErrUnsupported, ErrUnavailable, ErrConflict, ErrTimeout} {
					if errors.Is(err, s) {
						t.Fatalf("unexpected sentinel %v for %v", s, err)
					}
				}
			}
			if e.Name != tt.dbusName || e.Msg != tt.msg {
				t.Fatalf("name/msg not preserved: %+v", e)
			}
			if !strings.Contains(err.Error(), tt.msg) || !strings.Contains(err.Error(), "test op") {
				t.Fatalf("message must carry op and NM text: %v", err)
			}
			if (e.Hint != "") != tt.hint {
				t.Fatalf("hint=%q want present=%v", e.Hint, tt.hint)
			}
		})
	}
}

func TestWrapDBusPolkitHint(t *testing.T) {
	err := wrapDBus("activate x", &dbus.Error{Name: "org.freedesktop.NetworkManager.PermissionDenied", Body: []any{"Not authorized"}})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "polkit") || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("permission errors must hint at a session polkit agent: %v", err)
	}
	// Pointer form and non-D-Bus errors.
	if err := wrapDBus("op", context.Canceled); !errors.Is(err, context.Canceled) || strings.HasPrefix(err.Error(), "nm: op: context canceled") == false {
		t.Fatalf("plain error wrapping: %v", err)
	}
	if err := wrapDBus("op", dbus.ErrClosed); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed conn must be unavailable: %v", err)
	}
	if wrapDBus("op", nil) != nil {
		t.Fatal("nil in, nil out")
	}
	if e := wrapDBus("op", dbus.Error{Name: "x.y.Z"}); !strings.Contains(e.Error(), "x.y.Z") {
		t.Fatalf("empty body must fall back to the name: %v", e)
	}
}

func TestNewErrAndPolkitAuthActions(t *testing.T) {
	err := newErr("scan wlan0", ErrPermissionDenied, "denied")
	if !errors.Is(err, ErrPermissionDenied) || !strings.Contains(err.Error(), "polkit") {
		t.Fatalf("newErr permission: %v", err)
	}
	err = newErr("x", nil, "plain")
	if err.Error() != "nm: x: plain" {
		t.Fatalf("plain newErr: %q", err.Error())
	}
	got := PolkitAuthActions(map[string]string{
		permNetworkControl: "auth", permWifiScan: "yes", permModifySystem: "auth", permModifyOwn: "no",
	})
	if len(got) != 2 {
		t.Fatalf("PolkitAuthActions=%v", got)
	}
	if PolkitAuthActions(nil) != nil {
		t.Fatal("nil in, nil out")
	}
}

func TestIsUnknownMethod(t *testing.T) {
	if !isUnknownMethod(dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod", Body: []any{"no"}}) {
		t.Fatal("value form")
	}
	if !isUnknownMethod(&dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}) {
		t.Fatal("pointer form")
	}
	if isUnknownMethod(dbus.Error{Name: "org.freedesktop.NetworkManager.PermissionDenied"}) || isUnknownMethod(nil) || isUnknownMethod(context.Canceled) {
		t.Fatal("false positives")
	}
}
