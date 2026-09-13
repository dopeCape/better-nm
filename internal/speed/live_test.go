//go:build live

package speed

import (
	"context"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// TestLiveCloudflareQuick runs a quick test against the real
// speed.cloudflare.com from this machine and prints the result.
//
//	go test -tags live -run TestLiveCloudflareQuick -v ./internal/speed/
func TestLiveCloudflareQuick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tester := New(ProviderCloudflare)
	res, err := tester.Run(ctx, core.SpeedOptions{Quick: true}, func(p core.SpeedProgress) {
		t.Logf("progress: phase=%-8s %6.1f%%  %8.1f Mbit/s  %d bytes", p.Phase, p.Percent, p.Mbps, p.Bytes)
	})
	if err != nil {
		t.Fatalf("live cloudflare: %v (partial: %+v)", err, res)
	}
	t.Logf("result: colo=%s latency=%.1f ms jitter=%.1f ms down=%.1f Mbit/s up=%.1f Mbit/s bytes=%d (%.1f MB) in %s",
		res.Server, res.LatencyMs, res.JitterMs, res.DownloadMbps, res.UploadMbps,
		res.BytesMoved, float64(res.BytesMoved)/1e6, res.Duration.Round(time.Millisecond))
	if res.DownloadMbps <= 0 || res.UploadMbps <= 0 || res.LatencyMs <= 0 {
		t.Errorf("implausible result: %+v", res)
	}
	if res.BytesMoved > QuickMaxBytes {
		t.Errorf("quick test moved %d bytes, over the %d cap", res.BytesMoved, QuickMaxBytes)
	}
}
