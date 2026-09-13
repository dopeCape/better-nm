package cli

import (
	"io"
	"sync"
)

// safeWriter filters what bnm prints so text from the outside world (an
// SSID, an mDNS hostname, a tailnet peer name, a Docker network name) cannot
// steer the terminal: it drops every control character and escape sequence
// except newline, tab and our own colour codes (SGR: ESC [ ... m). JSON output
// is unaffected because encoding/json escapes control characters itself.
//
// A write may end inside a possible SGR sequence; that tail is held until the
// next write or Flush decides what it was.
type safeWriter struct {
	mu   sync.Mutex
	w    io.Writer
	hold []byte // an incomplete "ESC [ params" seen at the end of the last write
}

func newSafeWriter(w io.Writer) *safeWriter { return &safeWriter{w: w} }

func (s *safeWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := p
	if len(s.hold) > 0 {
		in = append(s.hold, p...)
		s.hold = nil
	}
	out, hold := sanitizeBytes(in, false)
	s.hold = hold
	if _, err := s.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes whatever an incomplete escape sequence left pending (minus the
// ESC itself, so it shows as plain text rather than doing anything).
func (s *safeWriter) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hold) == 0 {
		return nil
	}
	out, _ := sanitizeBytes(s.hold, true)
	s.hold = nil
	_, err := s.w.Write(out)
	return err
}

// sanitizeBytes returns in with control characters and non-SGR escape
// sequences removed. Unless final is set, a trailing incomplete SGR candidate
// is returned as hold instead of being decided now.
func sanitizeBytes(in []byte, final bool) (out, hold []byte) {
	out = make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		b := in[i]
		switch {
		case b == 0x1b:
			end, complete := sgrEnd(in, i)
			if complete {
				out = append(out, in[i:end]...)
				i = end - 1
				continue
			}
			if end == len(in) && !final {
				// could still become "ESC [ 3 1 m" with the next write
				return out, append([]byte(nil), in[i:]...)
			}
			// a bare ESC or another kind of sequence: drop the ESC, show the rest
		case b == '\n' || b == '\t':
			out = append(out, b)
		case b < 0x20 || b == 0x7f:
			// C0 control (CR, BS, BEL, ...): dropped
		case b == 0xc2 && i+1 < len(in) && in[i+1] >= 0x80 && in[i+1] <= 0x9f:
			// C1 control (8-bit CSI/OSC), UTF-8 encoded: dropped
			i++
		default:
			out = append(out, b)
		}
	}
	return out, nil
}

// sgrEnd looks at an ESC at in[i]. It returns the index just past a complete
// SGR sequence (ESC [ digits ; : m) with complete=true; otherwise complete is
// false and end is where scanning stopped (len(in) means "ran out of bytes
// while it still could have been one").
func sgrEnd(in []byte, i int) (end int, complete bool) {
	if i+1 >= len(in) {
		return len(in), false
	}
	if in[i+1] != '[' {
		return i + 1, false
	}
	for j := i + 2; j < len(in); j++ {
		switch c := in[j]; {
		case c >= '0' && c <= '9', c == ';', c == ':':
		case c == 'm':
			return j + 1, true
		default:
			return j, false
		}
	}
	return len(in), false
}
