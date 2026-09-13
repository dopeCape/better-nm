//go:build live

package monitor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLiveProbes sends real echoes from this machine (read-only: nothing on
// the host changes). Run with:
//
//	go test -tags live -run TestLiveProbes -v ./internal/monitor/
func TestLiveProbes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	icmp := &ICMPProber{}
	rtt, loss, method, err := icmp.Probe(ctx, "1.1.1.1")
	if errors.Is(err, ErrPingSocketDenied) {
		t.Logf("icmp: %v", err)
	} else if err != nil {
		t.Fatalf("icmp probe: %v", err)
	} else {
		t.Logf("icmp 1.1.1.1: rtt=%.2f ms loss=%.0f%% method=%s", rtt, loss*100, method)
		if loss < 1 && rtt <= 0 {
			t.Errorf("implausible rtt %v with loss %v", rtt, loss)
		}
	}

	tcp := &TCPProber{}
	rtt, loss, method, err = tcp.Probe(ctx, "1.1.1.1")
	if err != nil {
		t.Fatalf("tcp probe: %v", err)
	}
	t.Logf("tcp  1.1.1.1: rtt=%.2f ms loss=%.0f%% method=%s", rtt, loss*100, method)

	auto := NewAutoProber(nil)
	rtt, loss, method, err = auto.Probe(ctx, "8.8.8.8")
	if err != nil {
		t.Fatalf("auto probe: %v", err)
	}
	t.Logf("auto 8.8.8.8: rtt=%.2f ms loss=%.0f%% method=%s (prober now %s)", rtt, loss*100, method, auto.Method())

	ms, err := DNSProbe(ctx, "")
	if err != nil {
		t.Fatalf("dns probe: %v", err)
	}
	t.Logf("dns (system resolver): %.2f ms", ms)
}
