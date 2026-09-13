package speed

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dopeCape/better-nm/internal/core"
)

// ErrUnknownProvider is returned by a tester built for a provider bnm does not know.
var ErrUnknownProvider = core.Errorf(core.KindInvalid, "use cloudflare or iperf3", "speed: unknown provider")

// Option configures New.
type Option func(*options)

type options struct {
	httpClient  *http.Client
	baseURL     string
	iperfBinary string
}

// WithHTTPClient sets the HTTP client used by the Cloudflare provider.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.httpClient = c } }

// WithBaseURL points the Cloudflare provider at another base URL (tests, mirrors).
func WithBaseURL(u string) Option { return func(o *options) { o.baseURL = u } }

// WithIperf3Binary sets the iperf3 executable path.
func WithIperf3Binary(path string) Option { return func(o *options) { o.iperfBinary = path } }

// New returns the tester for provider ("" = cloudflare). An unknown provider
// yields a tester whose Run fails with ErrUnknownProvider, so callers can
// build once and report at Run time.
func New(provider string, opts ...Option) core.SpeedTester {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	switch provider {
	case "", ProviderCloudflare:
		c := &Cloudflare{Client: o.httpClient}
		if o.baseURL != "" {
			if base, err := parseBase(o.baseURL); err == nil {
				c.BaseURL = base
			} else {
				return errTester{err}
			}
		}
		return c
	case ProviderIperf3:
		return &Iperf3{Binary: o.iperfBinary}
	default:
		return errTester{fmt.Errorf("%w: %q", ErrUnknownProvider, provider)}
	}
}

// errTester always fails with a fixed error.
type errTester struct{ err error }

func (e errTester) Run(context.Context, core.SpeedOptions, func(core.SpeedProgress)) (core.SpeedResult, error) {
	return core.SpeedResult{}, e.err
}
