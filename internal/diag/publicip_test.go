package diag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleTrace = `fl=123abc
h=1.1.1.1
ip=203.0.113.7
ts=1757851200.123
visit_scheme=https
uag=bnm
colo=BOM
sliver=none
http=http/2
loc=IN
tls=TLSv1.3
sni=off
warp=off
gateway=off
rbi=off
kex=X25519
`

func TestParseTrace(t *testing.T) {
	p, err := parseTrace(strings.NewReader(sampleTrace))
	if err != nil {
		t.Fatal(err)
	}
	if p.IP != "203.0.113.7" || p.Colo != "BOM" || p.Location != "IN" || p.Warp != "off" {
		t.Errorf("parsed %+v", p)
	}
	if _, err := parseTrace(strings.NewReader("fl=1\nh=x\n")); err == nil {
		t.Error("missing ip= must error")
	}
}

func TestPublicIPFallback(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleTrace))
	}))
	defer good.Close()

	old := traceURLs
	traceURLs = []string{bad.URL + "/cdn-cgi/trace", good.URL + "/cdn-cgi/trace"}
	t.Cleanup(func() { traceURLs = old })

	p, err := PublicIP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.IP != "203.0.113.7" || p.Via != good.URL+"/cdn-cgi/trace" {
		t.Errorf("got %+v", p)
	}

	traceURLs = []string{bad.URL + "/cdn-cgi/trace"}
	if _, err := PublicIP(context.Background()); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("all-fail error = %v", err)
	}
}

func TestPublicIPLive(t *testing.T) {
	online(t)
	p, err := PublicIP(context.Background())
	if err != nil {
		t.Skipf("public ip unavailable: %v", err)
	}
	if p.IP == "" || p.Via == "" {
		t.Errorf("got %+v", p)
	}
	t.Logf("public ip %+v", p)
}
