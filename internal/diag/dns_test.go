package diag

import (
	"context"
	"net"
	"testing"
	"time"
)

func online(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := net.DefaultResolver.LookupIP(ctx, "ip4", "one.one.one.one"); err != nil {
		t.Skipf("offline: %v", err)
	}
}

func TestDNSLookupInvalid(t *testing.T) {
	if _, err := DNSLookup(context.Background(), "example.com", "", "SRV"); err == nil {
		t.Error("unsupported type must error")
	}
	if _, err := DNSLookup(context.Background(), "", "", "A"); err == nil {
		t.Error("empty name must error")
	}
}

func TestDNSLookupServerAddr(t *testing.T) {
	// A lookup against a closed port fails fast; we only check the server normalisation.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	tests := []struct{ in, want string }{
		{"1.1.1.1", "1.1.1.1:53"},
		{"1.1.1.1:5353", "1.1.1.1:5353"},
		{"[::1]:53", "[::1]:53"},
		{"::1", "[::1]:53"},
	}
	for _, tc := range tests {
		ans, err := DNSLookup(ctx, "example.invalid", tc.in, "A")
		if err != nil {
			t.Fatal(err)
		}
		if ans.Server != tc.want {
			t.Errorf("server %q -> %q, want %q", tc.in, ans.Server, tc.want)
		}
	}
}

func TestDNSLookupLive(t *testing.T) {
	online(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tests := []struct {
		qtype  string
		server string
	}{
		{"A", ""}, {"AAAA", ""}, {"MX", ""}, {"TXT", ""}, {"NS", ""}, {"CNAME", ""},
		{"A", "1.1.1.1"}, {"a", "1.1.1.1:53"},
	}
	for _, tc := range tests {
		ans, err := DNSLookup(ctx, "cloudflare.com", tc.server, tc.qtype)
		if err != nil {
			t.Fatalf("%s: %v", tc.qtype, err)
		}
		if ans.Error != "" {
			t.Logf("%s via %q: %s (skipping)", tc.qtype, tc.server, ans.Error)
			continue
		}
		if ans.Duration <= 0 {
			t.Errorf("%s: no duration", tc.qtype)
		}
		if tc.qtype != "CNAME" && len(ans.Answers) == 0 {
			t.Errorf("%s: no answers", tc.qtype)
		}
		t.Logf("%s %s via %q: %v in %v", ans.Name, ans.Type, ans.Server, ans.Answers, ans.Duration)
	}

	ans, err := DNSLookup(ctx, "nxdomain.example.invalid", "", "A")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Error == "" {
		t.Error("expected an error string for NXDOMAIN")
	}
}
