package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

func openMem(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestMigrationsRecorded(t *testing.T) {
	db := openMem(t)
	v, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Fatalf("schema version = %d, want %d", v, len(migrations))
	}
}

func TestSamplesRoundTrip(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	if err := db.AddSample(ctx, core.Sample{}); err == nil {
		t.Fatal("AddSample with empty key should fail")
	}

	for i := 0; i < 5; i++ {
		s := core.Sample{
			Time: base.Add(time.Duration(i) * time.Second), NetworkKey: "wifi:home", Anchor: "gateway",
			AnchorAddr: "192.168.1.1", RTTms: float64(10 + i), Loss: 0, DNSms: 12.5, Method: "icmp",
		}
		if err := db.AddSample(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Another anchor and another key should not leak into the query.
	_ = db.AddSample(ctx, core.Sample{Time: base, NetworkKey: "wifi:home", Anchor: "1.1.1.1", RTTms: 20, Method: "icmp"})
	_ = db.AddSample(ctx, core.Sample{Time: base, NetworkKey: "wifi:work", Anchor: "gateway", RTTms: 30, Method: "tcp"})

	tests := []struct {
		name   string
		anchor string
		limit  int
		want   []float64 // rtt sequence, oldest first
	}{
		{"all rows oldest first", "gateway", 0, []float64{10, 11, 12, 13, 14}},
		{"limit keeps newest, oldest first", "gateway", 2, []float64{13, 14}},
		{"limit larger than rows", "gateway", 50, []float64{10, 11, 12, 13, 14}},
		{"every anchor", "", 0, []float64{10, 20, 11, 12, 13, 14}},
		{"unknown anchor", "9.9.9.9", 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.Samples(ctx, "wifi:home", tc.anchor, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d", len(got), len(tc.want))
			}
			for i, s := range got {
				if s.RTTms != tc.want[i] {
					t.Errorf("row %d rtt = %v, want %v", i, s.RTTms, tc.want[i])
				}
			}
		})
	}

	got, err := db.Samples(ctx, "wifi:home", "gateway", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := core.Sample{Time: base.Add(4 * time.Second), NetworkKey: "wifi:home", Anchor: "gateway",
		AnchorAddr: "192.168.1.1", RTTms: 14, Loss: 0, DNSms: 12.5, Method: "icmp"}
	if !got[0].Time.Equal(want.Time) {
		t.Errorf("time = %v, want %v", got[0].Time, want.Time)
	}
	got[0].Time = want.Time
	if got[0] != want {
		t.Errorf("sample = %+v, want %+v", got[0], want)
	}

	n, err := db.SampleCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("SampleCount = %d, want 7", n)
	}
}

func TestBaselines(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	if err := db.PutBaseline(ctx, core.Baseline{}); err == nil {
		t.Fatal("PutBaseline with empty key should fail")
	}

	put := func(anchor string, rtt float64, state core.BaselineState) {
		t.Helper()
		if err := db.PutBaseline(ctx, core.Baseline{
			NetworkKey: "wifi:home", Anchor: anchor, State: state, SampleCount: 42,
			BaselineRTT: rtt, BaselineLoss: 0.01, CurrentRTT: rtt + 1, CurrentLoss: 0.02, CurrentDNS: 9,
			Since: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("8.8.8.8", 20, core.BaselineOK)
	put("gateway", 2, core.BaselineOK)
	put("1.1.1.1", 19, core.BaselineLearning)
	put("1.1.1.1", 21, core.BaselineOK) // upsert

	got, err := db.Baselines(ctx, "wifi:home")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d baselines, want 3", len(got))
	}
	if got[0].Anchor != "gateway" || got[1].Anchor != "1.1.1.1" || got[2].Anchor != "8.8.8.8" {
		t.Errorf("order = %s,%s,%s; want gateway first then sorted", got[0].Anchor, got[1].Anchor, got[2].Anchor)
	}
	if got[1].BaselineRTT != 21 || got[1].State != core.BaselineOK {
		t.Errorf("upsert not applied: %+v", got[1])
	}
	if !got[0].Since.Equal(now) || !got[0].UpdatedAt.Equal(now) {
		t.Errorf("timestamps lost: since=%v updated=%v", got[0].Since, got[0].UpdatedAt)
	}
	if got[0].SampleCount != 42 || got[0].CurrentDNS != 9 || got[0].CurrentLoss != 0.02 {
		t.Errorf("fields lost: %+v", got[0])
	}

	other, err := db.Baselines(ctx, "wifi:other")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Errorf("unknown key returned %d baselines", len(other))
	}

	if err := db.DeleteBaselines(ctx, "wifi:home"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.Baselines(ctx, "wifi:home")
	if len(got) != 0 {
		t.Errorf("after delete got %d baselines", len(got))
	}
}

func TestSpeedResults(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		r := core.SpeedResult{
			Time: base.Add(time.Duration(i) * time.Minute), NetworkKey: "wifi:home", Provider: "cloudflare",
			Server: "BOM", DownloadMbps: float64(100 + i), UploadMbps: 50, LatencyMs: 12.5, JitterMs: 1.5,
			BytesMoved: 1 << 20, Duration: 3 * time.Second, Quick: i == 0,
		}
		if err := db.AddSpeedResult(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AddSpeedResult(ctx, core.SpeedResult{Time: base, NetworkKey: "wifi:work", Provider: "iperf3", DownloadMbps: 900}); err != nil {
		t.Fatal(err)
	}

	got, err := db.SpeedResults(ctx, "wifi:home", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].DownloadMbps != 100 || got[2].DownloadMbps != 102 {
		t.Fatalf("unexpected rows: %+v", got)
	}
	if !got[0].Quick || got[1].Quick {
		t.Errorf("quick flag lost")
	}
	if got[0].Duration != 3*time.Second || got[0].BytesMoved != 1<<20 || got[0].Server != "BOM" {
		t.Errorf("fields lost: %+v", got[0])
	}

	got, err = db.SpeedResults(ctx, "wifi:home", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DownloadMbps != 102 {
		t.Errorf("limit should keep newest: %+v", got)
	}

	all, err := db.SpeedResults(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("all keys: got %d, want 4", len(all))
	}
}

func TestEvents(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	if err := db.AddEvent(ctx, core.Event{}); err == nil {
		t.Fatal("AddEvent without type should fail")
	}
	events := []core.Event{
		{Time: base, Type: core.EventConnected, NetworkKey: "wifi:home", Title: "Connected", Urgency: "low"},
		{Time: base.Add(time.Minute), Type: core.EventDegraded, NetworkKey: "wifi:home", Title: "Degraded",
			Body: "Latency to 1.1.1.1 is 64 ms, usually 21 ms", Urgency: "normal",
			Data: map[string]string{"anchor": "1.1.1.1", "current_rtt_ms": "64"}},
		{Time: base.Add(2 * time.Minute), Type: core.EventRecovered, NetworkKey: "wifi:home", Title: "Recovered"},
	}
	for _, e := range events {
		if err := db.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.Events(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[1].Data["anchor"] != "1.1.1.1" || got[1].Data["current_rtt_ms"] != "64" {
		t.Errorf("data lost: %+v", got[1].Data)
	}
	if got[0].Data != nil {
		t.Errorf("empty data should decode to nil, got %+v", got[0].Data)
	}
	if got[1].Body != events[1].Body || got[1].Urgency != "normal" || !got[1].Time.Equal(events[1].Time) {
		t.Errorf("fields lost: %+v", got[1])
	}

	got, err = db.Events(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != core.EventDegraded || got[1].Type != core.EventRecovered {
		t.Errorf("limit should keep newest two oldest-first: %+v", got)
	}
}

func TestSettings(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	if _, ok, err := db.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("Get missing = ok=%v err=%v", ok, err)
	}
	if err := db.Set(ctx, "", "x"); err == nil {
		t.Fatal("Set with empty key should fail")
	}
	if err := db.Set(ctx, "anchors", "1.1.1.1,8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	if err := db.Set(ctx, "anchors", "9.9.9.9"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := db.Get(ctx, "anchors")
	if err != nil || !ok || v != "9.9.9.9" {
		t.Fatalf("Get = %q ok=%v err=%v", v, ok, err)
	}
	if err := db.Set(ctx, "empty", ""); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := db.Get(ctx, "empty"); !ok || v != "" {
		t.Errorf("empty value should be distinguishable from missing: ok=%v v=%q", ok, v)
	}
	if err := db.Delete(ctx, "anchors"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.Get(ctx, "anchors"); ok {
		t.Error("Delete did not remove the key")
	}
	if err := db.Delete(ctx, "never-there"); err != nil {
		t.Errorf("Delete of a missing key should be a no-op, got %v", err)
	}
}

func TestPrune(t *testing.T) {
	db, err := Open(":memory:", WithRetention(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Retention() != 24*time.Hour {
		t.Fatalf("Retention = %v", db.Retention())
	}
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	fresh := now.Add(-time.Hour)

	for _, tm := range []time.Time{old, old.Add(time.Minute), fresh, now} {
		if err := db.AddSample(ctx, core.Sample{Time: tm, NetworkKey: "k", Anchor: "gateway", RTTms: 1}); err != nil {
			t.Fatal(err)
		}
		if err := db.AddEvent(ctx, core.Event{Time: tm, Type: core.EventConnected}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	samples, _ := db.Samples(ctx, "k", "gateway", 0)
	if len(samples) != 2 {
		t.Errorf("after prune %d samples remain, want 2", len(samples))
	}
	events, _ := db.Events(ctx, 0)
	if len(events) != 2 {
		t.Errorf("after prune %d events remain, want 2", len(events))
	}
}

func TestDefaultRetentionOptionIgnoresZero(t *testing.T) {
	db, err := Open(":memory:", WithRetention(0))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Retention() != DefaultRetention {
		t.Errorf("Retention = %v, want default", db.Retention())
	}
}

func TestConcurrentAddSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bnm.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	const workers, perWorker = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				s := core.Sample{Time: time.Now(), NetworkKey: "k", Anchor: "gateway", RTTms: float64(w*100 + i), Method: "icmp"}
				if err := db.AddSample(ctx, s); err != nil {
					errs <- err
				}
				// Interleave reads with writes.
				if _, err := db.Samples(ctx, "k", "gateway", 5); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	n, err := db.SampleCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != workers*perWorker {
		t.Errorf("SampleCount = %d, want %d", n, workers*perWorker)
	}
}

func TestReopenAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bnm.db")
	ctx := context.Background()
	stamp := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddSample(ctx, core.Sample{Time: stamp, NetworkKey: "wifi:home", Anchor: "gateway", RTTms: 3, Method: "icmp"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutBaseline(ctx, core.Baseline{NetworkKey: "wifi:home", Anchor: "gateway", State: core.BaselineOK, BaselineRTT: 3, UpdatedAt: stamp}); err != nil {
		t.Fatal(err)
	}
	if err := db.Set(ctx, "interval", "30s"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddEvent(ctx, core.Event{Time: stamp, Type: core.EventConnected, NetworkKey: "wifi:home"}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddSpeedResult(ctx, core.SpeedResult{Time: stamp, NetworkKey: "wifi:home", Provider: "cloudflare", DownloadMbps: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	v, err := db2.SchemaVersion(ctx)
	if err != nil || v != len(migrations) {
		t.Fatalf("schema version after reopen = %d err=%v", v, err)
	}
	samples, err := db2.Samples(ctx, "wifi:home", "gateway", 0)
	if err != nil || len(samples) != 1 || samples[0].RTTms != 3 || !samples[0].Time.Equal(stamp) {
		t.Errorf("samples after reopen = %+v err=%v", samples, err)
	}
	bl, err := db2.Baselines(ctx, "wifi:home")
	if err != nil || len(bl) != 1 || bl[0].State != core.BaselineOK {
		t.Errorf("baselines after reopen = %+v err=%v", bl, err)
	}
	if val, ok, err := db2.Get(ctx, "interval"); err != nil || !ok || val != "30s" {
		t.Errorf("setting after reopen = %q ok=%v err=%v", val, ok, err)
	}
	if ev, err := db2.Events(ctx, 0); err != nil || len(ev) != 1 {
		t.Errorf("events after reopen = %+v err=%v", ev, err)
	}
	if sr, err := db2.SpeedResults(ctx, "wifi:home", 0); err != nil || len(sr) != 1 {
		t.Errorf("speed results after reopen = %+v err=%v", sr, err)
	}
}

func TestFileDBUsesWAL(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bnm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var busy int
	if err := db.db.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busy)
	}
}

func TestClosedDBReturnsErrors(t *testing.T) {
	db := openMem(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Samples(context.Background(), "k", "a", 0); err == nil {
		t.Fatal("expected error from a closed DB")
	}
	if err := db.Set(context.Background(), "k", "v"); err == nil {
		t.Fatal("expected error from a closed DB")
	}
}
