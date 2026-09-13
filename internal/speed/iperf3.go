package speed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// ErrIperf3Missing means the iperf3 binary is not on PATH (core.KindUnsupported;
// the hint says how to install it).
var ErrIperf3Missing = core.Errorf(core.KindUnsupported,
	"install iperf3 (apt install iperf3 / dnf install iperf3 / pacman -S iperf3 / nix-env -iA nixpkgs.iperf3)",
	"speed: iperf3 not found")

// ErrServerRequired means the iperf3 provider was used without a host.
var ErrServerRequired = core.Errorf(core.KindInvalid, "pass --server host[:port] or set speed.iperf3_server", "speed: iperf3 needs a server")

// Iperf3 implements core.SpeedTester by running the distro's iperf3 client
// twice (upload, then -R for download) with -J and parsing the JSON.
type Iperf3 struct {
	Binary   string        // default "iperf3" (looked up on PATH)
	Duration time.Duration // per direction; default 5 s (quick: 2 s)
	Streams  int           // -P; default 1
}

var _ core.SpeedTester = (*Iperf3)(nil)

// Iperf3 defaults.
const (
	Iperf3DefaultPort     = 5201
	iperf3DefaultDuration = 5 * time.Second
	iperf3QuickDuration   = 2 * time.Second
)

func (p *Iperf3) binary() string {
	if p.Binary != "" {
		return p.Binary
	}
	return "iperf3"
}

// Run executes both directions against opts.Server.
func (p *Iperf3) Run(ctx context.Context, opts core.SpeedOptions, progress func(core.SpeedProgress)) (core.SpeedResult, error) {
	if progress == nil {
		progress = func(core.SpeedProgress) {}
	}
	if opts.Server == "" {
		return core.SpeedResult{}, ErrServerRequired
	}
	bin, err := exec.LookPath(p.binary())
	if err != nil {
		return core.SpeedResult{}, fmt.Errorf("%w: %v", ErrIperf3Missing, err)
	}
	host, port := splitServer(opts.Server)
	dur := p.Duration
	if dur <= 0 {
		dur = iperf3DefaultDuration
		if opts.Quick {
			dur = iperf3QuickDuration
		}
	}
	start := time.Now()
	res := core.SpeedResult{Time: start, NetworkKey: opts.NetworkKey, Provider: ProviderIperf3,
		Server: net.JoinHostPort(host, strconv.Itoa(port)), Quick: opts.Quick}

	progress(core.SpeedProgress{Phase: "upload"})
	up, err := p.runOnce(ctx, bin, host, port, dur, false)
	if err != nil {
		return res, err
	}
	res.UploadMbps = up.sentMbps()
	res.LatencyMs = up.rttMs()
	res.BytesMoved += up.bytes()
	progress(core.SpeedProgress{Phase: "upload", Mbps: res.UploadMbps, Percent: 100, Bytes: res.BytesMoved})

	progress(core.SpeedProgress{Phase: "download"})
	down, err := p.runOnce(ctx, bin, host, port, dur, true)
	if err != nil {
		return res, err
	}
	res.DownloadMbps = down.receivedMbps()
	res.BytesMoved += down.bytes()
	if res.LatencyMs == 0 {
		res.LatencyMs = down.rttMs()
	}
	progress(core.SpeedProgress{Phase: "download", Mbps: res.DownloadMbps, Percent: 100, Bytes: res.BytesMoved})
	res.Duration = time.Since(start)
	progress(core.SpeedProgress{Phase: "done", Percent: 100, Bytes: res.BytesMoved})
	return res, nil
}

func (p *Iperf3) runOnce(ctx context.Context, bin, host string, port int, dur time.Duration, reverse bool) (*iperf3Output, error) {
	args := []string{"-c", host, "-p", strconv.Itoa(port), "-J", "-t", strconv.Itoa(int(dur.Seconds()))}
	if p.Streams > 1 {
		args = append(args, "-P", strconv.Itoa(p.Streams))
	}
	if reverse {
		args = append(args, "-R")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = time.Second // do not hang on a stuck child holding stdout
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	out, parseErr := parseIperf3(stdout.Bytes())
	if parseErr == nil && out.Error != "" {
		return nil, fmt.Errorf("speed: iperf3 %s: %s", host, out.Error)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("speed: iperf3 %s: %w", host, ctx.Err())
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && parseErr == nil {
			msg = out.Error
		}
		if msg == "" {
			msg = runErr.Error()
		}
		return nil, fmt.Errorf("speed: iperf3 %s: %s", host, msg)
	}
	if parseErr != nil {
		return nil, parseErr
	}
	return out, nil
}

// iperf3Output is the subset of `iperf3 -J` bnm reads.
type iperf3Output struct {
	Error string `json:"error"`
	End   struct {
		SumSent struct {
			Bytes         int64   `json:"bytes"`
			BitsPerSecond float64 `json:"bits_per_second"`
			Retransmits   int     `json:"retransmits"`
		} `json:"sum_sent"`
		SumReceived struct {
			Bytes         int64   `json:"bytes"`
			BitsPerSecond float64 `json:"bits_per_second"`
		} `json:"sum_received"`
		Sum struct { // UDP
			JitterMs    float64 `json:"jitter_ms"`
			LostPercent float64 `json:"lost_percent"`
		} `json:"sum"`
		Streams []struct {
			Sender struct {
				MeanRTT int `json:"mean_rtt"` // microseconds
			} `json:"sender"`
		} `json:"streams"`
	} `json:"end"`
}

func parseIperf3(b []byte) (*iperf3Output, error) {
	var out iperf3Output
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, errors.New("speed: iperf3 produced no output")
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("speed: parse iperf3 json: %w", err)
	}
	return &out, nil
}

func (o *iperf3Output) sentMbps() float64     { return o.End.SumSent.BitsPerSecond / 1e6 }
func (o *iperf3Output) receivedMbps() float64 { return o.End.SumReceived.BitsPerSecond / 1e6 }
func (o *iperf3Output) bytes() int64 {
	if o.End.SumReceived.Bytes > o.End.SumSent.Bytes {
		return o.End.SumReceived.Bytes
	}
	return o.End.SumSent.Bytes
}

func (o *iperf3Output) rttMs() float64 {
	if len(o.End.Streams) == 0 {
		return 0
	}
	return float64(o.End.Streams[0].Sender.MeanRTT) / 1000
}

func splitServer(s string) (string, int) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return strings.Trim(s, "[]"), Iperf3DefaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		port = Iperf3DefaultPort
	}
	return host, port
}
