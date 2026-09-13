//go:build live

package diag

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveAll runs every diagnostic on this machine (read-only; the sweep
// sends ICMP echoes on the LAN). BNM_LIVE_DEVICE selects the device
// (default wlp4s0).
func TestLiveAll(t *testing.T) {
	dev := os.Getenv("BNM_LIVE_DEVICE")
	if dev == "" {
		dev = "wlp4s0"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("LANHosts", func(t *testing.T) {
		start := time.Now()
		hosts, err := LANHosts(ctx, dev, true)
		t.Logf("LANHosts(%s, sweep) -> %d hosts in %v, err=%v", dev, len(hosts), time.Since(start), err)
		for _, h := range hosts {
			flag := ""
			if h.Self {
				flag += " self"
			}
			if h.Gateway {
				flag += " gateway"
			}
			t.Logf("  %-40s %-17s %-10s %-30s seen=%s%s", h.IP, h.MAC, h.State, h.Hostname, h.Seen.Format(time.TimeOnly), flag)
		}
	})

	t.Run("ListeningPorts", func(t *testing.T) {
		ports, err := ListeningPorts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d listening sockets", len(ports))
		for _, p := range ports {
			t.Logf("  %-5s %-40s %-6d uid=%-6d user=%-12s pid=%-7d %s", p.Proto, p.Addr, p.Port, p.UID, p.User, p.PID, p.Process)
		}
	})

	t.Run("Routes", func(t *testing.T) {
		routes, err := Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range routes {
			t.Logf("  v%d table=%-4d %-30s via %-20s dev %-16s metric=%-5d proto=%-8s scope=%s", r.Family, r.Table, r.Dest, r.Gateway, r.Device, r.Metric, r.Proto, r.Scope)
		}
	})

	t.Run("DNSLookup", func(t *testing.T) {
		for _, q := range []string{"A", "AAAA", "MX", "TXT", "NS", "CNAME"} {
			a, err := DNSLookup(ctx, "cloudflare.com", "", q)
			t.Logf("  %s %s: %v (%v) err=%q %v", a.Name, a.Type, a.Answers, a.Duration, a.Error, err)
		}
		a, _ := DNSLookup(ctx, "www.google.com", "1.1.1.1", "CNAME")
		t.Logf("  %s %s via %s: %v (%v) err=%q", a.Name, a.Type, a.Server, a.Answers, a.Duration, a.Error)
	})

	t.Run("PublicIP", func(t *testing.T) {
		p, err := PublicIP(ctx)
		t.Logf("  %+v err=%v", p, err)
	})
}
