package core

import (
	"errors"
	"fmt"
	"testing"
)

type hinted struct{ msg, hint string }

func (h hinted) Error() string { return h.msg }
func (h hinted) Hint() string  { return h.hint }

func TestErrorKinds(t *testing.T) {
	cause := errors.New("dbus: access denied")
	tests := []struct {
		name string
		err  error
		kind ErrorKind
		is   error
		hint string
		msg  string
	}{
		{"nil", nil, "", nil, "", ""},
		{"errorf", Errorf(KindNotFound, "run bnm profiles", "profile %s missing", "x"), KindNotFound, ErrNotFound, "run bnm profiles", "profile x missing"},
		{"wrap", Wrap(KindPermission, "add a polkit rule", cause), KindPermission, ErrPermission, "add a polkit rule", "dbus: access denied"},
		{"wrap keeps cause", Wrap(KindPermission, "", cause), KindPermission, cause, "", "dbus: access denied"},
		{"wrapped again", fmt.Errorf("nm: activate: %w", Errorf(KindUnsupported, "", "no wifi")), KindUnsupported, ErrUnsupported, "", "nm: activate: no wifi"},
		{"sentinel", fmt.Errorf("x: %w", ErrConflict), KindConflict, ErrConflict, "", "x: conflict"},
		{"plain", errors.New("boom"), KindInternal, nil, "", "boom"},
		{"duck hint", hinted{"no ip", "install iproute2"}, KindInternal, nil, "install iproute2", "no ip"},
		{"unavailable", Errorf(KindUnavailable, "", "tailscaled down"), KindUnavailable, ErrUnavailable, "", "tailscaled down"},
		{"invalid", Errorf(KindInvalid, "", "bad"), KindInvalid, ErrInvalid, "", "bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindOf(tt.err); got != tt.kind {
				t.Errorf("KindOf = %q, want %q", got, tt.kind)
			}
			if tt.is != nil && !errors.Is(tt.err, tt.is) {
				t.Errorf("errors.Is(%v, %v) false", tt.err, tt.is)
			}
			if got := HintOf(tt.err); got != tt.hint {
				t.Errorf("HintOf = %q, want %q", got, tt.hint)
			}
			if tt.err != nil && tt.err.Error() != tt.msg {
				t.Errorf("Error() = %q, want %q", tt.err.Error(), tt.msg)
			}
		})
	}
	if Wrap(KindInvalid, "", nil) != nil {
		t.Error("Wrap(nil) must be nil")
	}
	e := &Error{Kind: KindInternal, Msg: "m", Err: errors.New("c")}
	if e.Error() != "m: c" {
		t.Errorf("Error() = %q", e.Error())
	}
}
