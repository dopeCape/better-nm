package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/fake"
	"github.com/dopeCape/better-nm/internal/monitor"
	"github.com/dopeCape/better-nm/internal/store"
)

// slowProber answers a fixed RTT per address; rtt may change between rounds.
type slowProber struct {
	mu   sync.Mutex
	rtts map[string]float64
}

func (p *slowProber) set(addr string, rtt float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rtts[addr] = rtt
}

func (p *slowProber) Probe(_ context.Context, addr string) (float64, float64, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if rtt, ok := p.rtts[addr]; ok {
		return rtt, 0, monitor.MethodICMP, nil
	}
	return 20, 0, monitor.MethodICMP, nil
}

// With the real monitor and the real store wired like bnmd does, a degraded
// event must land in history exactly once (the monitor used to store it and
// the daemon stored it again).
func TestMonitorEventStoredOnce(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	key := "wifi:" + fake.HomeSSID
	// 40 rounds of 20 ms history so the baseline is "ok" from the start.
	start := time.Now().Add(-40 * 30 * time.Second)
	for i := 0; i < 40; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		for _, a := range []struct {
			anchor, addr string
			rtt          float64
		}{{monitor.GatewayAnchor, "192.168.1.1", 2}, {"1.1.1.1", "1.1.1.1", 20}, {"8.8.8.8", "8.8.8.8", 21}} {
			s := core.Sample{Time: at, NetworkKey: key, Anchor: a.anchor, AnchorAddr: a.addr, RTTms: a.rtt, DNSms: -1, Method: monitor.MethodICMP}
			if err := st.AddSample(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
	}

	p := &slowProber{rtts: map[string]float64{"192.168.1.1": 2, "1.1.1.1": 60, "8.8.8.8": 61}}
	mon := monitor.New(monitor.Config{
		Interval: 5 * time.Millisecond,
		Anchors:  []string{"1.1.1.1", "8.8.8.8"},
		DNS:      func(context.Context, string) (float64, error) { return 12, nil },
	}, st, p, slog.New(slog.DiscardHandler))

	nm := fake.NewNM()
	d, err := New(Options{
		NM:         nm,
		Monitor:    monitorRunner{mon},
		Store:      st,
		Config:     config.Default(),
		ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		Logger:     slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := d.Subscribe()
	defer unsub()
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- d.Run(rctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitEvent(t, ch, core.EventDegraded, 5*time.Second)
	// Let a few more rounds run: a duplicate would show up here.
	time.Sleep(50 * time.Millisecond)
	evs, err := st.Events(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == core.EventDegraded {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("degraded stored %d times, want 1: %+v", n, evs)
	}
}

// monitorRunner adapts monitor.Monitor.Run to the daemon's Monitor port, as cmd/bnmd does.
type monitorRunner struct{ *monitor.Monitor }

func (m monitorRunner) Run(ctx context.Context) error {
	m.Monitor.Run(ctx)
	return nil
}
