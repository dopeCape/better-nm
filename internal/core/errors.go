package core

import (
	"errors"
	"fmt"
)

// ErrorKind classifies a failure so surfaces can map it to an HTTP status or a
// human message without string matching.
type ErrorKind string

const (
	KindPermission  ErrorKind = "permission"  // polkit/operator denied; Hint says the one-time fix
	KindNotFound    ErrorKind = "not-found"   // profile, device, VPN, peer missing
	KindUnsupported ErrorKind = "unsupported" // backend not installed, capability absent
	KindInvalid     ErrorKind = "invalid"     // bad input from the outside world
	KindConflict    ErrorKind = "conflict"    // already running / already exists
	KindUnavailable ErrorKind = "unavailable" // backend daemon not running, bus absent
	KindInternal    ErrorKind = "internal"
)

// Sentinels so packages can `errors.Is` without importing each other's types.
var (
	ErrPermission  = errors.New("permission denied")
	ErrNotFound    = errors.New("not found")
	ErrUnsupported = errors.New("unsupported")
	ErrInvalid     = errors.New("invalid")
	ErrConflict    = errors.New("conflict")
	ErrUnavailable = errors.New("unavailable")
)

// Error is a typed failure with an optional human hint (what one-time step fixes it).
type Error struct {
	Kind ErrorKind
	Msg  string
	Hint string
	Err  error
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil && e.Msg != "":
		return e.Msg + ": " + e.Err.Error()
	case e.Err != nil:
		return e.Err.Error()
	}
	return e.Msg
}

// Unwrap lets errors.Is match both the wrapped cause and the kind sentinel.
func (e *Error) Unwrap() []error {
	var out []error
	if s := sentinelFor(e.Kind); s != nil {
		out = append(out, s)
	}
	if e.Err != nil {
		out = append(out, e.Err)
	}
	return out
}

// Errorf builds a typed error. hint may be "".
func Errorf(kind ErrorKind, hint, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...), Hint: hint}
}

// Wrap attaches a kind (and hint) to an existing error; nil stays nil.
func Wrap(kind ErrorKind, hint string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Hint: hint, Err: err}
}

func sentinelFor(k ErrorKind) error {
	switch k {
	case KindPermission:
		return ErrPermission
	case KindNotFound:
		return ErrNotFound
	case KindUnsupported:
		return ErrUnsupported
	case KindInvalid:
		return ErrInvalid
	case KindConflict:
		return ErrConflict
	case KindUnavailable:
		return ErrUnavailable
	}
	return nil
}

// KindOf classifies any error: a *Error's Kind, a sentinel match, else KindInternal.
func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	switch {
	case errors.Is(err, ErrPermission):
		return KindPermission
	case errors.Is(err, ErrNotFound):
		return KindNotFound
	case errors.Is(err, ErrUnsupported):
		return KindUnsupported
	case errors.Is(err, ErrInvalid):
		return KindInvalid
	case errors.Is(err, ErrConflict):
		return KindConflict
	case errors.Is(err, ErrUnavailable):
		return KindUnavailable
	}
	return KindInternal
}

// HintOf returns the hint carried by err, if any. Any error with a
// `Hint() string` method is honoured so backends need not use *Error.
func HintOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) && e.Hint != "" {
		return e.Hint
	}
	var h interface{ Hint() string }
	if errors.As(err, &h) {
		return h.Hint()
	}
	return ""
}
