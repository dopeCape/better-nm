package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/fake"
)

func TestSafeWriter(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{"plain", []string{"hello\n"}, "hello\n"},
		{"sgr kept", []string{"\x1b[32m●\x1b[0m ok\n"}, "\x1b[32m●\x1b[0m ok\n"},
		{"sgr with colons", []string{"\x1b[38:2:1:2:3mx\x1b[m"}, "\x1b[38:2:1:2:3mx\x1b[m"},
		{"osc title dropped", []string{"a\x1b]0;pwned\x07b\n"}, "a]0;pwnedb\n"},
		{"csi cursor dropped", []string{"a\x1b[2J\x1b[Hb"}, "a[2J[Hb"},
		{"c0 dropped, tab and newline kept", []string{"a\rb\bc\td\n"}, "abc\td\n"},
		{"c1 dropped", []string{"a\u009b31mb"}, "a31mb"},
		{"del dropped", []string{"a\x7fb"}, "ab"},
		{"utf8 kept", []string{"café ▂▄▆█ 日本\n"}, "café ▂▄▆█ 日本\n"},
		{"sgr split across writes", []string{"x\x1b[3", "2my\x1b[0m"}, "x\x1b[32my\x1b[0m"},
		{"esc split then not sgr", []string{"x\x1b", "]0;t\x07y"}, "x]0;ty"},
		{"trailing esc flushed", []string{"x\x1b["}, "x["},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := newSafeWriter(&buf)
			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				if err != nil || n != len(s) {
					t.Fatalf("write %q: n=%d err=%v", s, n, err)
				}
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A hostile SSID must not be able to retitle the window, move the cursor or
// hide the rest of the table.
func TestHostileSSIDCannotSteerTheTerminal(t *testing.T) {
	r := newRig(t)
	evil := "Evil\x1b]0;pwned\x07\x1b[2J\rNet"
	r.nm.AddAP(fake.WifiDevice, core.WifiNetwork{SSID: evil, Strength: 40, Security: core.SecOpen, Band: "2.4", Channel: 1})
	for _, args := range [][]string{{"wifi", "list"}, {"wifi", "connect", evil, "--json"}, {"wifi", "saved"}, {"status"}} {
		res := r.ok(args...)
		for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\r"} {
			if strings.Contains(res.out, bad) {
				t.Errorf("bnm %s printed %q:\n%q", strings.Join(args, " "), bad, res.out)
			}
		}
		if !strings.Contains(res.out, "Evil") {
			t.Errorf("bnm %s lost the visible part of the name:\n%s", strings.Join(args, " "), res.out)
		}
	}
	// the name reaches errors and hints on stderr too
	res := r.run("profile", "show", "nosuchthing")
	if strings.Contains(res.err, "\x1b]") || strings.Contains(res.err, "\x07") {
		t.Errorf("stderr carried an escape: %q", res.err)
	}
	// --json is untouched: encoding/json escapes the bytes itself
	if !strings.Contains(r.ok("wifi", "list", "--json").out, `\u001b]0;pwned`) {
		t.Error("json should carry the escaped SSID verbatim")
	}
}
