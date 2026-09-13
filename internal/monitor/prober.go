package monitor

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Probe methods, as stored in core.Sample.Method.
const (
	MethodICMP = "icmp"
	MethodTCP  = "tcp"
)

// Prober measures round-trip time and loss to one address.
type Prober interface {
	// Probe sends the prober's echoes to addr (an IP or host name) and
	// returns the median RTT in ms (-1 when every echo was lost), the loss
	// fraction 0..1, and the method used. err is only for failures to probe
	// at all (no socket, no resolution), never for a silent target.
	Probe(ctx context.Context, addr string) (rttMs float64, loss float64, method string, err error)
}

// ErrPingSocketDenied means the kernel refused an unprivileged ICMP socket.
// The fix is a one-time sysctl; Debian/Ubuntu do not ship it.
var ErrPingSocketDenied = errors.New("monitor: ping socket denied by kernel; " +
	"add `net.ipv4.ping_group_range = 0 2147483647` via /etc/sysctl.d/50-bnm.conf " +
	"(then `sysctl --system`), or bnm falls back to TCP connect timing")

// ErrNoAddress means the target name resolved to nothing.
var ErrNoAddress = errors.New("monitor: no address for target")

const (
	defaultEchoes      = 3
	defaultEchoTimeout = 1500 * time.Millisecond
	defaultEchoGap     = 100 * time.Millisecond
	echoPayloadSize    = 32
)

// ---- ICMP ---------------------------------------------------------------

// ICMPProber sends ICMP echo requests over the kernel's unprivileged
// "ping" datagram socket (net.ipv4.ping_group_range); no CAP_NET_RAW.
type ICMPProber struct {
	Count   int           // echoes per Probe (default 3)
	Timeout time.Duration // per echo (default 1.5 s)
	Gap     time.Duration // pause between echoes (default 100 ms)
	// Resolver resolves host names; nil uses net.DefaultResolver.
	Resolver *net.Resolver

	seq uint32
}

var _ Prober = (*ICMPProber)(nil)

func (p *ICMPProber) count() int {
	if p.Count > 0 {
		return p.Count
	}
	return defaultEchoes
}

func (p *ICMPProber) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return defaultEchoTimeout
}

func (p *ICMPProber) gap() time.Duration {
	if p.Gap > 0 {
		return p.Gap
	}
	return defaultEchoGap
}

// Probe implements Prober.
func (p *ICMPProber) Probe(ctx context.Context, addr string) (float64, float64, string, error) {
	ip, err := resolveIP(ctx, p.Resolver, addr)
	if err != nil {
		return -1, 1, MethodICMP, err
	}
	network, listenAddr, proto, echoType := "udp4", "0.0.0.0", ipv4.ICMPTypeEcho.Protocol(), icmp.Type(ipv4.ICMPTypeEcho)
	if ip.To4() == nil {
		network, listenAddr, proto, echoType = "udp6", "::", ipv6.ICMPTypeEchoRequest.Protocol(), icmp.Type(ipv6.ICMPTypeEchoRequest)
	}
	conn, err := icmp.ListenPacket(network, listenAddr)
	if err != nil {
		if isPermissionError(err) {
			return -1, 1, MethodICMP, fmt.Errorf("%w: %v", ErrPingSocketDenied, err)
		}
		return -1, 1, MethodICMP, fmt.Errorf("monitor: icmp listen %s: %w", network, err)
	}
	defer conn.Close()

	// Unprivileged ping sockets overwrite the echo identifier with the
	// socket's port, so replies are matched on sequence + a random token.
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return -1, 1, MethodICMP, fmt.Errorf("monitor: icmp token: %w", err)
	}
	dst := &net.UDPAddr{IP: ip}
	n := p.count()
	var rtts []float64
	sent := 0
	buf := make([]byte, 1500)
	for i := 0; i < n; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return median(rtts), lossOf(rtts, sent), MethodICMP, nil
			case <-time.After(p.gap()):
			}
		}
		sent++
		seq := int(atomic.AddUint32(&p.seq, 1) & 0xffff)
		body := make([]byte, echoPayloadSize)
		copy(body, token[:])
		start := time.Now()
		binary.BigEndian.PutUint64(body[8:], uint64(start.UnixNano()))
		msg := icmp.Message{Type: echoType, Code: 0, Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: seq, Data: body}}
		wb, err := msg.Marshal(nil)
		if err != nil {
			return -1, 1, MethodICMP, fmt.Errorf("monitor: icmp marshal: %w", err)
		}
		if _, err := conn.WriteTo(wb, dst); err != nil {
			// Unreachable network etc.: this echo is lost, keep going.
			continue
		}
		deadline := start.Add(p.timeout())
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if rtt, ok := p.awaitReply(conn, buf, proto, seq, token[:], deadline); ok {
			rtts = append(rtts, rtt)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return median(rtts), lossOf(rtts, sent), MethodICMP, nil
}

// awaitReply reads until the matching echo reply or the deadline.
func (p *ICMPProber) awaitReply(conn *icmp.PacketConn, buf []byte, proto, seq int, token []byte, deadline time.Time) (float64, bool) {
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return 0, false
		}
		n, _, err := conn.ReadFrom(buf)
		now := time.Now()
		if err != nil {
			return 0, false // timeout or socket error: lost
		}
		msg, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		if msg.Type != ipv4.ICMPTypeEchoReply && msg.Type != ipv6.ICMPTypeEchoReply {
			continue // destination unreachable etc.: keep waiting for the deadline
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok || echo.Seq != seq || len(echo.Data) < 16 || string(echo.Data[:8]) != string(token) {
			continue
		}
		sent := time.Unix(0, int64(binary.BigEndian.Uint64(echo.Data[8:16])))
		rtt := float64(now.Sub(sent)) / float64(time.Millisecond)
		if rtt < 0 {
			rtt = 0
		}
		return rtt, true
	}
}

func isPermissionError(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, os.ErrPermission)
}

// ---- TCP ----------------------------------------------------------------

// TCPProber times TCP connects; the fallback when ping sockets are denied or
// ICMP is filtered. It tries the ports in order and sticks with the first
// one that answers for the rest of the probe.
type TCPProber struct {
	Count   int           // connects per Probe (default 3)
	Timeout time.Duration // per connect (default 1.5 s)
	Gap     time.Duration // pause between connects (default 100 ms)
	Ports   []int         // default 443, 80, 53
	// Dialer overrides the dialer (tests).
	Dialer *net.Dialer
}

var _ Prober = (*TCPProber)(nil)

// DefaultTCPPorts are tried in order.
var DefaultTCPPorts = []int{443, 80, 53}

func (p *TCPProber) count() int {
	if p.Count > 0 {
		return p.Count
	}
	return defaultEchoes
}

func (p *TCPProber) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return defaultEchoTimeout
}

func (p *TCPProber) gap() time.Duration {
	if p.Gap > 0 {
		return p.Gap
	}
	return defaultEchoGap
}

func (p *TCPProber) ports() []int {
	if len(p.Ports) > 0 {
		return p.Ports
	}
	return DefaultTCPPorts
}

// Probe implements Prober.
func (p *TCPProber) Probe(ctx context.Context, addr string) (float64, float64, string, error) {
	host, explicitPort, hasPort := splitHostPort(addr)
	if host == "" {
		return -1, 1, MethodTCP, fmt.Errorf("%w: %q", ErrNoAddress, addr)
	}
	ports := p.ports()
	if hasPort {
		ports = []int{explicitPort}
	}
	dialer := p.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	n := p.count()
	var rtts []float64
	sent := 0
	chosen := 0
	for i := 0; i < n; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return median(rtts), lossOf(rtts, sent), MethodTCP, nil
			case <-time.After(p.gap()):
			}
		}
		sent++
		try := ports
		if chosen != 0 {
			try = []int{chosen}
		}
		for _, port := range try {
			rtt, ok := p.dialOnce(ctx, dialer, net.JoinHostPort(host, strconv.Itoa(port)))
			if ok {
				rtts = append(rtts, rtt)
				chosen = port
				break
			}
			if ctx.Err() != nil {
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	return median(rtts), lossOf(rtts, sent), MethodTCP, nil
}

func (p *TCPProber) dialOnce(ctx context.Context, d *net.Dialer, hostport string) (float64, bool) {
	dctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	start := time.Now()
	c, err := d.DialContext(dctx, "tcp", hostport)
	if err != nil {
		return 0, false
	}
	rtt := float64(time.Since(start)) / float64(time.Millisecond)
	_ = c.Close()
	return rtt, true
}

func splitHostPort(addr string) (host string, port int, ok bool) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return addr, 0, false
	}
	return h, n, true
}

// ---- Auto ---------------------------------------------------------------

// AutoProber uses ICMP until the kernel denies a ping socket, then TCP for
// the rest of the process.
type AutoProber struct {
	ICMP   Prober
	TCP    Prober
	Logger *slog.Logger

	useTCP atomic.Bool
	once   sync.Once
}

var _ Prober = (*AutoProber)(nil)

// NewAutoProber builds an AutoProber with the default ICMP and TCP probers.
func NewAutoProber(logger *slog.Logger) *AutoProber {
	return &AutoProber{ICMP: &ICMPProber{}, TCP: &TCPProber{}, Logger: logger}
}

// Method reports which method the next Probe will use.
func (a *AutoProber) Method() string {
	if a.useTCP.Load() {
		return MethodTCP
	}
	return MethodICMP
}

// Probe implements Prober.
func (a *AutoProber) Probe(ctx context.Context, addr string) (float64, float64, string, error) {
	if !a.useTCP.Load() {
		rtt, loss, method, err := a.ICMP.Probe(ctx, addr)
		if err == nil || !errors.Is(err, ErrPingSocketDenied) {
			return rtt, loss, method, err
		}
		a.useTCP.Store(true)
		a.once.Do(func() {
			if a.Logger != nil {
				a.Logger.Warn("icmp ping socket denied, using tcp connect timing from now on", "err", err)
			}
		})
	}
	return a.TCP.Probe(ctx, addr)
}

// ---- DNS ----------------------------------------------------------------

// dnsNames are resolved in rotation so caches do not flatter the number.
var dnsNames = []string{"example.com", "cloudflare.com", "wikipedia.org"}

var dnsRotation uint32

// DNSTimeout bounds one DNSProbe.
const DNSTimeout = 3 * time.Second

// DNSProbe measures how long the resolver takes to answer an A/AAAA query
// for a rotating public name. resolver is "host[:port]" or "" for the
// system resolver. Returns milliseconds.
func DNSProbe(ctx context.Context, resolver string) (float64, error) {
	r := &net.Resolver{PreferGo: true}
	if resolver != "" {
		host, port, ok := splitHostPort(resolver)
		if !ok {
			host, port = resolver, 53
		}
		target := net.JoinHostPort(host, strconv.Itoa(port))
		r.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: DNSTimeout}
			return d.DialContext(ctx, network, target)
		}
	}
	name := dnsNames[int(atomic.AddUint32(&dnsRotation, 1)-1)%len(dnsNames)]
	ctx, cancel := context.WithTimeout(ctx, DNSTimeout)
	defer cancel()
	start := time.Now()
	addrs, err := r.LookupIPAddr(ctx, name)
	if err != nil {
		return -1, fmt.Errorf("monitor: dns %s: %w", name, err)
	}
	if len(addrs) == 0 {
		return -1, fmt.Errorf("monitor: dns %s: %w", name, ErrNoAddress)
	}
	return float64(time.Since(start)) / float64(time.Millisecond), nil
}

// ---- helpers ------------------------------------------------------------

func resolveIP(ctx context.Context, r *net.Resolver, addr string) (net.IP, error) {
	if ip := net.ParseIP(addr); ip != nil {
		return ip, nil
	}
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupIPAddr(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("monitor: resolve %s: %w", addr, err)
	}
	for _, a := range addrs { // prefer v4: the ping socket path is best tested there
		if a.IP.To4() != nil {
			return a.IP, nil
		}
	}
	if len(addrs) > 0 {
		return addrs[0].IP, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNoAddress, addr)
}

func lossOf(rtts []float64, sent int) float64 {
	if sent <= 0 {
		return 1
	}
	return float64(sent-len(rtts)) / float64(sent)
}
