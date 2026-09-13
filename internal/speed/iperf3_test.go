package speed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

const iperf3JSON = `{
  "start": {"connected": [{"remote_host": "10.0.0.2", "remote_port": 5201}]},
  "end": {
    "sum_sent": {"bytes": 1130000000, "bits_per_second": 904000000.5, "retransmits": 3},
    "sum_received": {"bytes": 1128000000, "bits_per_second": 902400000.0},
    "streams": [{"sender": {"mean_rtt": 1234, "max_rtt": 2000, "min_rtt": 900}}]
  }
}`

// fakeIperf3 writes a shell script that records its args and prints canned
// JSON; with -R it reports a different rate so the two directions are
// distinguishable.
func fakeIperf3(t *testing.T) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "iperf3")
	argsLog = filepath.Join(dir, "args.log")
	script := `#!/bin/sh
echo "$@" >> "` + argsLog + `"
case "$*" in
  *-R*) echo '{"end":{"sum_sent":{"bytes":10,"bits_per_second":1000000},"sum_received":{"bytes":500000000,"bits_per_second":400000000},"streams":[{"sender":{"mean_rtt":5000}}]}}' ;;
  *) echo '` + strings.ReplaceAll(iperf3JSON, "\n", "") + `' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func TestParseIperf3(t *testing.T) {
	out, err := parseIperf3([]byte(iperf3JSON))
	if err != nil {
		t.Fatal(err)
	}
	if out.sentMbps() != 904.0000005 || out.receivedMbps() != 902.4 {
		t.Errorf("mbps = %v / %v", out.sentMbps(), out.receivedMbps())
	}
	if out.rttMs() != 1.234 {
		t.Errorf("rtt = %v", out.rttMs())
	}
	if out.bytes() != 1130000000 {
		t.Errorf("bytes = %d", out.bytes())
	}
	if _, err := parseIperf3(nil); err == nil {
		t.Error("empty output should fail")
	}
	if _, err := parseIperf3([]byte("not json")); err == nil {
		t.Error("garbage should fail")
	}
	out, err = parseIperf3([]byte(`{"error":"unable to connect to server: Connection refused"}`))
	if err != nil || out.Error == "" {
		t.Errorf("error field not parsed: %v %v", out, err)
	}
}

func TestIperf3RunWithFakeBinary(t *testing.T) {
	bin, argsLog := fakeIperf3(t)
	p := &Iperf3{Binary: bin, Duration: 3 * time.Second}
	var phases []string
	res, err := p.Run(context.Background(), core.SpeedOptions{Server: "10.0.0.2:5202", NetworkKey: "wired:x"}, func(pr core.SpeedProgress) {
		phases = append(phases, pr.Phase)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.UploadMbps != 904.0000005 || res.DownloadMbps != 400 {
		t.Errorf("mbps = up %v down %v", res.UploadMbps, res.DownloadMbps)
	}
	if res.LatencyMs != 1.234 || res.Provider != ProviderIperf3 || res.Server != "10.0.0.2:5202" || res.NetworkKey != "wired:x" {
		t.Errorf("result = %+v", res)
	}
	if res.BytesMoved != 1130000000+500000000 {
		t.Errorf("BytesMoved = %d", res.BytesMoved)
	}
	if strings.Join(phases, ",") != "upload,upload,download,download,done" {
		t.Errorf("phases = %v", phases)
	}
	log, _ := os.ReadFile(argsLog)
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 2 {
		t.Fatalf("iperf3 invoked %d times: %q", len(lines), log)
	}
	if lines[0] != "-c 10.0.0.2 -p 5202 -J -t 3" {
		t.Errorf("upload args = %q", lines[0])
	}
	if lines[1] != "-c 10.0.0.2 -p 5202 -J -t 3 -R" {
		t.Errorf("download args = %q", lines[1])
	}
}

func TestIperf3DefaultsAndQuick(t *testing.T) {
	bin, argsLog := fakeIperf3(t)
	p := &Iperf3{Binary: bin, Streams: 4}
	if _, err := p.Run(context.Background(), core.SpeedOptions{Server: "host.example", Quick: true}, nil); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(argsLog)
	first := strings.Split(strings.TrimSpace(string(log)), "\n")[0]
	if first != "-c host.example -p 5201 -J -t 2 -P 4" {
		t.Errorf("args = %q", first)
	}
}

func TestIperf3ServerErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "iperf3")
	script := "#!/bin/sh\necho '{\"error\":\"unable to connect to server: Connection refused\"}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Iperf3{Binary: bin}
	_, err := p.Run(context.Background(), core.SpeedOptions{Server: "10.0.0.9"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("err = %v, want the server's message", err)
	}
}

func TestIperf3MissingBinary(t *testing.T) {
	p := &Iperf3{Binary: filepath.Join(t.TempDir(), "definitely-not-iperf3")}
	_, err := p.Run(context.Background(), core.SpeedOptions{Server: "10.0.0.2"}, nil)
	if !errors.Is(err, ErrIperf3Missing) {
		t.Fatalf("err = %v, want ErrIperf3Missing", err)
	}
	if !strings.Contains(core.HintOf(err), "install") {
		t.Errorf("error lacks an install hint: %v (hint %q)", err, core.HintOf(err))
	}
	if core.KindOf(err) != core.KindUnsupported {
		t.Errorf("kind = %s, want unsupported", core.KindOf(err))
	}
}

func TestIperf3NeedsServer(t *testing.T) {
	p := &Iperf3{Binary: "/nonexistent"}
	if _, err := p.Run(context.Background(), core.SpeedOptions{}, nil); !errors.Is(err, ErrServerRequired) {
		t.Errorf("err = %v, want ErrServerRequired", err)
	}
}

func TestIperf3ContextCancel(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "iperf3")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&Iperf3{Binary: bin}).Run(ctx, core.SpeedOptions{Server: "h"}, nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v after cancel", time.Since(start))
	}
}

func TestSplitServer(t *testing.T) {
	for in, want := range map[string]struct {
		host string
		port int
	}{
		"10.0.0.2":       {"10.0.0.2", 5201},
		"10.0.0.2:5202":  {"10.0.0.2", 5202},
		"[fe80::1]:7":    {"fe80::1", 7},
		"host:notaport":  {"host", 5201},
		"[fe80::1]":      {"fe80::1", 5201},
		"host.example":   {"host.example", 5201},
		"host.example:0": {"host.example", 5201},
	} {
		h, p := splitServer(in)
		if h != want.host || p != want.port {
			t.Errorf("splitServer(%q) = %q,%d; want %q,%d", in, h, p, want.host, want.port)
		}
	}
}
