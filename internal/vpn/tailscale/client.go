// Package tailscale drives a local tailscaled through its LocalAPI over the
// Unix socket, with a hand-rolled HTTP client and hand-written JSON subsets
// instead of importing tailscale.com (which would force Go 1.26 and add ~6.6 MB).
// Client is the raw API; Adapter maps it onto core.VPNAdapter and
// core.TailscaleControl.
//
// Tested with an httptest server bound to a temp Unix socket replaying
// recorded JSON (testdata/); `go test -tags live` additionally reads the real
// daemon's status and prefs, read-only.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"
)

// Socket candidates, in order. Fedora/Debian/Arch/NixOS all ship /run/tailscale.
var socketCandidates = []string{
	"/run/tailscale/tailscaled.sock",
	"/var/run/tailscale/tailscaled.sock",
}

// LocalAPIHost is the Host header tailscaled requires on Unix-socket requests.
const LocalAPIHost = "local-tailscaled.sock"

var (
	// ErrAccessDenied is returned when tailscaled answers 403: the calling uid
	// is neither root nor the configured operator (or the request was malformed).
	ErrAccessDenied = errors.New("tailscale: access denied")
	// ErrUnavailable is returned when the socket does not exist or nothing listens on it.
	ErrUnavailable = errors.New("tailscale: tailscaled is not running")
)

// HTTPError is a non-2xx LocalAPI answer other than 403.
type HTTPError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("tailscale: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// DefaultSocket returns the first existing socket candidate, or the first
// candidate when none exists (so error messages name a concrete path).
func DefaultSocket() string {
	for _, p := range socketCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return socketCandidates[0]
}

// Client talks LocalAPI over a Unix socket. The zero value is not usable; use New.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a Client for socket ("" = DefaultSocket()).
func New(socket string) *Client {
	if socket == "" {
		socket = DefaultSocket()
	}
	c := &Client{socket: socket}
	c.http = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", c.socket)
			},
			// Streams (watch-ipn-bus) must not be reused; keep the pool tiny.
			MaxIdleConns:    2,
			IdleConnTimeout: 30 * time.Second,
		},
	}
	return c
}

// Socket returns the socket path in use.
func (c *Client) Socket() string { return c.socket }

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+LocalAPIHost+"/localapi/v0/"+path, rd)
	if err != nil {
		return nil, fmt.Errorf("tailscale: %s %s: %w", method, path, err)
	}
	req.Host = LocalAPIHost
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do sends the request and classifies transport and HTTP errors. The caller
// owns the response body on success.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if isUnavailable(err) {
			return nil, fmt.Errorf("%w (socket %s): %v", ErrUnavailable, c.socket, err)
		}
		return nil, fmt.Errorf("tailscale: %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := string(bytes.TrimSpace(msg))
	// PATCH prefs errors come back as {"Error": "..."}.
	var rj struct{ Error string }
	if json.Unmarshal(msg, &rj) == nil && rj.Error != "" {
		text = rj.Error
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s %s: %s", ErrAccessDenied, method, path, text)
	}
	return nil, &HTTPError{Method: method, Path: path, Status: resp.StatusCode, Body: text}
}

// isUnavailable reports whether err means "no daemon behind the socket".
func isUnavailable(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTSOCK)
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("tailscale: GET %s: decode: %w", path, err)
	}
	return nil
}

// Status returns the daemon status including peers.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.getJSON(ctx, "status", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// StatusWithoutPeers returns the daemon status with the Peer map omitted.
func (c *Client) StatusWithoutPeers(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.getJSON(ctx, "status?peers=false", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Prefs returns the current preferences.
func (c *Client) Prefs(ctx context.Context) (*Prefs, error) {
	var p Prefs
	if err := c.getJSON(ctx, "prefs", &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// EditPrefs applies the fields of mp whose <Field>Set flag is true and returns
// the resulting prefs. Needs operator mode (ErrAccessDenied otherwise).
func (c *Client) EditPrefs(ctx context.Context, mp MaskedPrefs) (*Prefs, error) {
	body, err := json.Marshal(mp)
	if err != nil {
		return nil, fmt.Errorf("tailscale: encode prefs: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPatch, "prefs", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var p Prefs
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("tailscale: PATCH prefs: decode: %w", err)
	}
	return &p, nil
}

// SetUseExitNode toggles the previously selected exit node on or off.
func (c *Client) SetUseExitNode(ctx context.Context, enabled bool) (*Prefs, error) {
	resp, err := c.do(ctx, http.MethodPost, "set-use-exit-node-enabled?enabled="+strconv.FormatBool(enabled), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var p Prefs
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("tailscale: set-use-exit-node-enabled: decode: %w", err)
	}
	return &p, nil
}

// StartLoginInteractive asks tailscaled to begin a browser login. The URL
// arrives afterwards via Status.AuthURL or Notify.BrowseToURL.
func (c *Client) StartLoginInteractive(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPost, "login-interactive", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Logout logs the node out of its tailnet.
func (c *Client) Logout(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPost, "logout", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// WatchIPNBus streams Notify messages to fn until ctx ends, the stream closes,
// or fn returns false. It returns nil on a clean end (ctx done or fn stop) and
// the transport/decode error otherwise, so callers can decide to reconnect.
func (c *Client) WatchIPNBus(ctx context.Context, mask NotifyWatchOpt, fn func(Notify) bool) error {
	resp, err := c.do(ctx, http.MethodGet, "watch-ipn-bus?mask="+strconv.FormatUint(uint64(mask), 10), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var n Notify
		if err := dec.Decode(&n); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("tailscale: watch-ipn-bus: stream closed")
			}
			return fmt.Errorf("tailscale: watch-ipn-bus: %w", err)
		}
		if !fn(n) {
			return nil
		}
	}
}
