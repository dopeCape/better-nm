package diag

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// DNSLookup resolves name with the system resolver, or with server (host or
// host:port, default port 53) when given, and measures the round trip. qtype
// is one of A, AAAA, MX, TXT, CNAME, NS (case-insensitive; "" means A). A
// resolution failure is reported in the answer's Error field with a nil
// error; a non-nil error means the request itself was invalid.
func DNSLookup(ctx context.Context, name, server, qtype string) (core.DNSAnswer, error) {
	qtype = strings.ToUpper(strings.TrimSpace(qtype))
	if qtype == "" {
		qtype = "A"
	}
	ans := core.DNSAnswer{Name: name, Server: server, Type: qtype, Answers: []string{}}
	if name == "" {
		return ans, core.Errorf(core.KindInvalid, "", "diag: dns: empty name")
	}
	r := &net.Resolver{PreferGo: true}
	if server != "" {
		addr := server
		if _, _, err := net.SplitHostPort(server); err != nil {
			addr = net.JoinHostPort(strings.Trim(server, "[]"), "53")
		}
		ans.Server = addr
		r.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}
	}

	start := time.Now()
	var answers []string
	var err error
	switch qtype {
	case "A", "AAAA":
		network := "ip4"
		if qtype == "AAAA" {
			network = "ip6"
		}
		var ips []net.IP
		ips, err = r.LookupIP(ctx, network, name)
		for _, ip := range ips {
			answers = append(answers, ip.String())
		}
	case "MX":
		var mx []*net.MX
		mx, err = r.LookupMX(ctx, name)
		for _, m := range mx {
			answers = append(answers, fmt.Sprintf("%d %s", m.Pref, strings.TrimSuffix(m.Host, ".")))
		}
	case "TXT":
		answers, err = r.LookupTXT(ctx, name)
	case "CNAME":
		var cname string
		cname, err = r.LookupCNAME(ctx, name)
		if err == nil && cname != "" {
			answers = []string{strings.TrimSuffix(cname, ".")}
		}
	case "NS":
		var ns []*net.NS
		ns, err = r.LookupNS(ctx, name)
		for _, n := range ns {
			answers = append(answers, strings.TrimSuffix(n.Host, "."))
		}
	default:
		return ans, core.Errorf(core.KindInvalid, "use A, AAAA, MX, TXT, CNAME or NS", "diag: dns: unsupported type %q", qtype)
	}
	ans.Duration = time.Since(start)
	if err != nil {
		ans.Error = err.Error()
		return ans, nil
	}
	if qtype != "MX" && qtype != "TXT" {
		sort.Strings(answers)
	}
	if answers != nil {
		ans.Answers = answers
	}
	return ans, nil
}
