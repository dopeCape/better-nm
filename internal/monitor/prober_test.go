package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// listenTCP returns a loopback listener that accepts and closes connections.
func listenTCP(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// closedPort returns a loopback port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestTCPProberMeasuresLoopback(t *testing.T) {
	host, port := listenTCP(t)
	p := &TCPProber{Count: 3, Ports: []int{port}, Gap: time.Millisecond}
	rtt, loss, method, err := p.Probe(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if method != MethodTCP {
		t.Errorf("method = %s", method)
	}
	if loss != 0 {
		t.Errorf("loss = %v, want 0", loss)
	}
	if rtt < 0 || rtt > 500 {
		t.Errorf("rtt = %v ms, implausible for loopback", rtt)
	}
}

func TestTCPProberFallsBackThroughPorts(t *testing.T) {
	host, open := listenTCP(t)
	dead := closedPort(t)
	p := &TCPProber{Count: 3, Ports: []int{dead, open}, Gap: time.Millisecond, Timeout: 500 * time.Millisecond}
	rtt, loss, _, err := p.Probe(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if loss != 0 || rtt < 0 {
		t.Errorf("rtt=%v loss=%v; fallback port should have answered", rtt, loss)
	}
}

func TestTCPProberExplicitPortAndAllLost(t *testing.T) {
	dead := closedPort(t)
	p := &TCPProber{Count: 2, Gap: time.Millisecond, Timeout: 300 * time.Millisecond}
	rtt, loss, _, err := p.Probe(context.Background(), net.JoinHostPort("127.0.0.1", strconv.Itoa(dead)))
	if err != nil {
		t.Fatal(err)
	}
	if rtt != -1 || loss != 1 {
		t.Errorf("rtt=%v loss=%v, want -1/1 for a closed port", rtt, loss)
	}
	if _, _, _, err := p.Probe(context.Background(), ""); !errors.Is(err, ErrNoAddress) {
		t.Errorf("empty addr err = %v, want ErrNoAddress", err)
	}
}

func TestTCPProberHonoursContext(t *testing.T) {
	host, port := listenTCP(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &TCPProber{Count: 3, Ports: []int{port}, Gap: time.Second}
	start := time.Now()
	_, _, _, err := p.Probe(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("cancelled probe took %v", time.Since(start))
	}
}

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		in   string
		host string
		port int
		ok   bool
	}{
		{"1.1.1.1", "1.1.1.1", 0, false},
		{"1.1.1.1:443", "1.1.1.1", 443, true},
		{"[2606:4700::1111]:53", "2606:4700::1111", 53, true},
		{"2606:4700::1111", "2606:4700::1111", 0, false},
		{"host:notaport", "host:notaport", 0, false},
		{"host:0", "host:0", 0, false},
	}
	for _, tc := range tests {
		h, p, ok := splitHostPort(tc.in)
		if h != tc.host || p != tc.port || ok != tc.ok {
			t.Errorf("splitHostPort(%q) = %q,%d,%v; want %q,%d,%v", tc.in, h, p, ok, tc.host, tc.port, tc.ok)
		}
	}
}

func TestIsPermissionError(t *testing.T) {
	wrapped := &net.OpError{Op: "listen", Err: os.NewSyscallError("socket", syscall.EACCES)}
	if !isPermissionError(wrapped) {
		t.Error("EACCES through net.OpError not detected")
	}
	if !isPermissionError(fmt.Errorf("x: %w", syscall.EPERM)) {
		t.Error("EPERM not detected")
	}
	if isPermissionError(errors.New("other")) {
		t.Error("false positive")
	}
}

type fakeProber struct {
	calls atomic.Int32
	rtt   float64
	loss  float64
	err   error
	name  string
}

func (f *fakeProber) Probe(context.Context, string) (float64, float64, string, error) {
	f.calls.Add(1)
	return f.rtt, f.loss, f.name, f.err
}

func TestAutoProberFallsBackOnceOnDenied(t *testing.T) {
	icmp := &fakeProber{err: fmt.Errorf("wrap: %w", ErrPingSocketDenied), name: MethodICMP, rtt: -1, loss: 1}
	tcp := &fakeProber{rtt: 7, name: MethodTCP}
	a := &AutoProber{ICMP: icmp, TCP: tcp, Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	if a.Method() != MethodICMP {
		t.Fatalf("initial method = %s", a.Method())
	}
	for i := 0; i < 3; i++ {
		rtt, _, method, err := a.Probe(context.Background(), "1.1.1.1")
		if err != nil || method != MethodTCP || rtt != 7 {
			t.Fatalf("probe %d: rtt=%v method=%s err=%v", i, rtt, method, err)
		}
	}
	if icmp.calls.Load() != 1 {
		t.Errorf("icmp tried %d times, want 1", icmp.calls.Load())
	}
	if tcp.calls.Load() != 3 {
		t.Errorf("tcp used %d times, want 3", tcp.calls.Load())
	}
	if a.Method() != MethodTCP {
		t.Errorf("method after fallback = %s", a.Method())
	}
}

func TestAutoProberKeepsICMPOnOtherErrors(t *testing.T) {
	icmp := &fakeProber{err: errors.New("resolve failed"), name: MethodICMP}
	tcp := &fakeProber{rtt: 7, name: MethodTCP}
	a := &AutoProber{ICMP: icmp, TCP: tcp}
	if _, _, _, err := a.Probe(context.Background(), "nope"); err == nil {
		t.Fatal("expected the icmp error to surface")
	}
	if tcp.calls.Load() != 0 || a.Method() != MethodICMP {
		t.Errorf("fell back on a non-permission error")
	}
	icmp.err = nil
	icmp.rtt = 3
	rtt, _, method, err := a.Probe(context.Background(), "1.1.1.1")
	if err != nil || method != MethodICMP || rtt != 3 {
		t.Errorf("icmp not used: rtt=%v method=%s err=%v", rtt, method, err)
	}
}

func TestNewAutoProberDefaults(t *testing.T) {
	a := NewAutoProber(nil)
	if _, ok := a.ICMP.(*ICMPProber); !ok {
		t.Error("ICMP prober not the default")
	}
	if _, ok := a.TCP.(*TCPProber); !ok {
		t.Error("TCP prober not the default")
	}
}

// fakeDNS answers every A query with 192.0.2.1 on a loopback UDP socket and
// records the names asked.
type fakeDNS struct {
	addr  string
	mu    sync.Mutex
	names []string
}

func newFakeDNS(t *testing.T, delay time.Duration) *fakeDNS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	f := &fakeDNS{addr: pc.LocalAddr().String()}
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			hdr, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			f.mu.Lock()
			f.names = append(f.names, q.Name.String())
			f.mu.Unlock()
			time.Sleep(delay)
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true, RCode: dnsmessage.RCodeSuccess})
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if q.Type == dnsmessage.TypeA {
				_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}})
			}
			out, err := b.Finish()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(out, addr)
		}
	}()
	return f
}

func (f *fakeDNS) seen() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]bool{}
	for _, n := range f.names {
		m[n] = true
	}
	return m
}

func TestDNSProbeAgainstFakeResolver(t *testing.T) {
	f := newFakeDNS(t, 20*time.Millisecond)
	ms, err := DNSProbe(context.Background(), f.addr)
	if err != nil {
		t.Fatal(err)
	}
	if ms < 20 || ms > 3000 {
		t.Errorf("dns ms = %v, want >= 20 (the fake's delay)", ms)
	}
}

func TestDNSProbeRotatesNames(t *testing.T) {
	f := newFakeDNS(t, 0)
	for i := 0; i < len(dnsNames)*2; i++ {
		if _, err := DNSProbe(context.Background(), f.addr); err != nil {
			t.Fatal(err)
		}
	}
	seen := f.seen()
	for _, want := range dnsNames {
		if !seen[want+"."] {
			t.Errorf("name %s never queried; saw %v", want, seen)
		}
	}
}

func TestDNSProbeFailsFastOnDeadResolver(t *testing.T) {
	// A closed UDP port: the go resolver gets ICMP unreachable / times out.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ms, err := DNSProbe(ctx, addr)
	if err == nil {
		t.Fatal("expected an error from a dead resolver")
	}
	if ms != -1 {
		t.Errorf("ms = %v on error, want -1", ms)
	}
}

func TestResolveIPLiteral(t *testing.T) {
	ip, err := resolveIP(context.Background(), nil, "8.8.4.4")
	if err != nil || ip.String() != "8.8.4.4" {
		t.Errorf("resolveIP literal = %v, %v", ip, err)
	}
	ip, err = resolveIP(context.Background(), nil, "2001:4860:4860::8888")
	if err != nil || ip.To4() != nil {
		t.Errorf("resolveIP v6 literal = %v, %v", ip, err)
	}
}

func TestLossOf(t *testing.T) {
	if got := lossOf(nil, 3); got != 1 {
		t.Errorf("lossOf(none of 3) = %v", got)
	}
	if got := lossOf([]float64{1, 2}, 3); got < 0.33 || got > 0.34 {
		t.Errorf("lossOf(2 of 3) = %v", got)
	}
	if got := lossOf(nil, 0); got != 1 {
		t.Errorf("lossOf(0 sent) = %v", got)
	}
}
