// Package client is the Go client for bnmd's API: the CLI, TUI and desktop app
// talk to the daemon only through it. New finds the socket (starting bnmd
// detached when it is not running), checks the API version, and every route
// has one typed method. Streams (events, speed) decode Server-Sent Events into
// core types. Tested against internal/api over a temp socket with internal/fake
// behind it; auto-start is tested with a stub bnmd built by the test.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/paths"
	"github.com/dopeCape/better-nm/internal/version"
)

// DefaultStartTimeout is how long New waits for an auto-started daemon's socket.
const DefaultStartTimeout = 3 * time.Second

// Client talks to one bnmd over its Unix socket.
type Client struct {
	socket       string
	autoStart    bool
	daemonPath   string
	startTimeout time.Duration
	checkVersion bool
	log          *slog.Logger
	http         *http.Client
	started      bool
}

// Option configures New.
type Option func(*Client)

// WithSocket overrides the socket path (default paths.Socket()).
func WithSocket(path string) Option { return func(c *Client) { c.socket = path } }

// WithAutoStart controls whether New spawns bnmd when the socket is absent (default true).
func WithAutoStart(on bool) Option { return func(c *Client) { c.autoStart = on } }

// WithDaemonPath pins the bnmd binary to spawn (default: next to this
// executable, then $PATH).
func WithDaemonPath(path string) Option { return func(c *Client) { c.daemonPath = path } }

// WithStartTimeout bounds the wait for an auto-started daemon.
func WithStartTimeout(d time.Duration) Option { return func(c *Client) { c.startTimeout = d } }

// WithVersionCheck controls the api_version check in New (default true).
func WithVersionCheck(on bool) Option { return func(c *Client) { c.checkVersion = on } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// ErrVersionMismatch is returned by New when the daemon speaks another API major version.
type ErrVersionMismatch struct {
	Daemon, Client int
	DaemonVersion  string
}

func (e *ErrVersionMismatch) Error() string {
	return fmt.Sprintf("bnmd %s speaks API v%d but this client needs v%d; restart the daemon (bnm daemon restart) or update bnm",
		e.DaemonVersion, e.Daemon, e.Client)
}

// ErrNotRunning is returned when the daemon is not reachable and auto-start is off or failed.
type ErrNotRunning struct {
	Socket string
	Cause  error
}

func (e *ErrNotRunning) Error() string {
	return fmt.Sprintf("bnmd is not running (socket %s): %v", e.Socket, e.Cause)
}

func (e *ErrNotRunning) Unwrap() error { return e.Cause }

// APIError is a non-2xx answer from the daemon. errors.Is matches the core
// sentinel of its Code (core.ErrNotFound, core.ErrPermission, ...).
type APIError struct {
	Status  int
	Code    core.ErrorKind
	Message string
	Hint    string
}

func (e *APIError) Error() string {
	if e.Hint != "" {
		return e.Message + " (" + e.Hint + ")"
	}
	return e.Message
}

// Unwrap maps Code to core's sentinel.
func (e *APIError) Unwrap() error {
	return core.Wrap(e.Code, e.Hint, errors.New(e.Message))
}

// New connects to bnmd. Without a reachable socket it spawns bnmd (unless
// WithAutoStart(false)), waits for the socket, then verifies the API version.
func New(opts ...Option) (*Client, error) {
	c := &Client{
		socket:       paths.Socket(),
		autoStart:    true,
		startTimeout: DefaultStartTimeout,
		checkVersion: true,
		log:          slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	c.http = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.socket)
		},
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
		ResponseHeaderTimeout: 0,
	}}
	if err := c.ensureRunning(); err != nil {
		return nil, err
	}
	if c.checkVersion {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := c.Status(ctx)
		if err != nil {
			return nil, fmt.Errorf("client: status: %w", err)
		}
		if st.APIVersion != version.APIVersion {
			return nil, &ErrVersionMismatch{Daemon: st.APIVersion, Client: version.APIVersion, DaemonVersion: st.Version}
		}
	}
	return c, nil
}

// Socket is the path in use.
func (c *Client) Socket() string { return c.socket }

// StartedDaemon reports whether New spawned bnmd.
func (c *Client) StartedDaemon() bool { return c.started }

// Close drops idle connections.
func (c *Client) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

func (c *Client) reachable() error {
	conn, err := net.DialTimeout("unix", c.socket, 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (c *Client) ensureRunning() error {
	err := c.reachable()
	if err == nil {
		return nil
	}
	if !c.autoStart {
		return &ErrNotRunning{Socket: c.socket, Cause: err}
	}
	if serr := c.spawn(); serr != nil {
		return &ErrNotRunning{Socket: c.socket, Cause: serr}
	}
	c.started = true
	deadline := time.Now().Add(c.startTimeout)
	for {
		if err = c.reachable(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return &ErrNotRunning{Socket: c.socket, Cause: fmt.Errorf("started bnmd but the socket did not appear within %v (see %s): %w", c.startTimeout, logPath(), err)}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// --- transport ---------------------------------------------------------------------

func (c *Client) url(path string, q url.Values) string {
	u := "http://bnmd" + api.Prefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *Client) newRequest(ctx context.Context, method, path string, q url.Values, body any) (*http.Request, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte: // sent verbatim (Raw)
		rd = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("client: encode: %w", err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, q), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req, nil
}

// do performs a JSON round trip; out may be nil.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	req, err := c.newRequest(ctx, method, path, q, in)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.wrapTransport(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return decodeError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("client: decode %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) wrapTransport(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return &ErrNotRunning{Socket: c.socket, Cause: err}
	}
	return err
}

func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var er api.ErrorResponse
	if err := json.Unmarshal(body, &er); err != nil || er.Error == "" {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Code: kindForStatus(resp.StatusCode), Message: msg}
	}
	return &APIError{Status: resp.StatusCode, Code: core.ErrorKind(er.Code), Message: er.Error, Hint: er.Hint}
}

func kindForStatus(status int) core.ErrorKind {
	switch status {
	case http.StatusForbidden:
		return core.KindPermission
	case http.StatusNotFound:
		return core.KindNotFound
	case http.StatusNotImplemented:
		return core.KindUnsupported
	case http.StatusBadRequest:
		return core.KindInvalid
	case http.StatusConflict:
		return core.KindConflict
	case http.StatusServiceUnavailable:
		return core.KindUnavailable
	}
	return core.KindInternal
}

func logPath() string {
	return paths.StateDir() + string(os.PathSeparator) + "bnmd.log"
}
