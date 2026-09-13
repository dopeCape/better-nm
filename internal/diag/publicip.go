package diag

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

const publicIPTimeout = 5 * time.Second

// traceURLs are tried in order; tests override them.
var traceURLs = []string{
	"https://1.1.1.1/cdn-cgi/trace",
	"https://www.cloudflare.com/cdn-cgi/trace",
}

// httpClient is shared so tests can point it at a fake server.
var httpClient = &http.Client{Timeout: publicIPTimeout}

// PublicIP asks Cloudflare's trace endpoint what address the world sees, with
// the colo, country and WARP status it reports. Via is the URL that answered.
func PublicIP(ctx context.Context) (core.PublicIP, error) {
	var errs []error
	for _, u := range traceURLs {
		if err := ctx.Err(); err != nil {
			return core.PublicIP{}, err
		}
		p, err := fetchTrace(ctx, u)
		if err == nil {
			return p, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", u, err))
	}
	return core.PublicIP{}, fmt.Errorf("diag: public ip: %w", errors.Join(errs...))
}

func fetchTrace(ctx context.Context, u string) (core.PublicIP, error) {
	rctx, cancel := context.WithTimeout(ctx, publicIPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return core.PublicIP{}, err
	}
	req.Header.Set("User-Agent", "bnm")
	resp, err := httpClient.Do(req)
	if err != nil {
		return core.PublicIP{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return core.PublicIP{}, fmt.Errorf("status %s", resp.Status)
	}
	p, err := parseTrace(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return core.PublicIP{}, err
	}
	p.Via = u
	return p, nil
}

// parseTrace reads the key=value lines of a cdn-cgi/trace body.
func parseTrace(r io.Reader) (core.PublicIP, error) {
	var p core.PublicIP
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			p.IP = v
		case "colo":
			p.Colo = v
		case "loc":
			p.Location = v
		case "warp":
			p.Warp = v
		}
	}
	if err := sc.Err(); err != nil {
		return p, err
	}
	if p.IP == "" {
		return p, errors.New("no ip= line in trace")
	}
	return p, nil
}
