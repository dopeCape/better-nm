// Package api serves bnmd's HTTP/1.1 + JSON API over the Unix socket. Every
// route lives under /v1 and maps one-to-one onto a *daemon.Daemon method; the
// wire types in types.go are shared with internal/client. Errors are JSON
// {error, hint?, code} with the status derived from core.ErrorKind. Two routes
// stream Server-Sent Events: GET /events/stream and POST /speed. The /secrets
// routes let a surface answer NetworkManager's password prompts (see
// core.SecretBroker). Tested end to end through internal/client against a
// daemon built on internal/fake.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
	"github.com/dopeCape/better-nm/internal/diag"
	"github.com/dopeCape/better-nm/internal/version"
)

// maxBody bounds request bodies (an .ovpn with inline certs fits comfortably).
const maxBody = 4 << 20

// DefaultHeartbeat is the SSE keep-alive comment interval.
const DefaultHeartbeat = 15 * time.Second

// Server serves the API for one daemon.
type Server struct {
	d         *daemon.Daemon
	log       *slog.Logger
	heartbeat time.Duration
	mux       *http.ServeMux
}

// Option tunes a Server.
type Option func(*Server)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.log = l } }

// WithHeartbeat sets the SSE heartbeat interval.
func WithHeartbeat(d time.Duration) Option { return func(s *Server) { s.heartbeat = d } }

// New builds a Server over d.
func New(d *daemon.Daemon, opts ...Option) *Server {
	s := &Server{d: d, log: slog.Default(), heartbeat: DefaultHeartbeat}
	for _, o := range opts {
		o(s)
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s
}

// Handler is the routed handler (for tests and embedding).
func (s *Server) Handler() http.Handler { return s }

// Serve accepts on ln until ctx ends, then shuts down gracefully. In-flight
// streams see their request context cancelled so shutdown is prompt.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	base, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer scancel()
		if err := srv.Shutdown(sctx); err != nil {
			_ = srv.Close()
		}
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) routes() {
	m := s.mux
	p := func(pattern string) string {
		method, path, _ := strings.Cut(pattern, " ")
		return method + " " + Prefix + path
	}
	m.HandleFunc(p("GET /status"), s.status)
	m.HandleFunc(p("GET /devices"), s.devices)
	m.HandleFunc(p("GET /wifi"), s.wifi)
	m.HandleFunc(p("POST /wifi/scan"), s.wifiScan)
	m.HandleFunc(p("POST /wifi/connect"), s.wifiConnect)
	m.HandleFunc(p("POST /wifi/disconnect"), s.wifiDisconnect)
	m.HandleFunc(p("POST /wifi/forget"), s.wifiForget)
	m.HandleFunc(p("POST /wifi/enabled"), s.wifiEnabled)
	m.HandleFunc(p("GET /profiles"), s.profiles)
	m.HandleFunc(p("GET /profiles/{uuid}"), s.profile)
	m.HandleFunc(p("PUT /profiles/{uuid}/ip"), s.profileIP)
	m.HandleFunc(p("POST /profiles/{uuid}/activate"), s.profileActivate)
	m.HandleFunc(p("POST /profiles/{uuid}/deactivate"), s.profileDeactivate)
	m.HandleFunc(p("POST /profiles/{uuid}/autoconnect"), s.profileAutoconnect)
	m.HandleFunc(p("DELETE /profiles/{uuid}"), s.profileDelete)
	m.HandleFunc(p("GET /active"), s.active)
	m.HandleFunc(p("GET /vpn"), s.vpns)
	m.HandleFunc(p("POST /vpn/import"), s.vpnImport)
	m.HandleFunc(p("POST /vpn/tailscale/exit-node"), s.tsExitNode)
	m.HandleFunc(p("POST /vpn/tailscale/exit-node/enabled"), s.tsExitNodeEnabled)
	m.HandleFunc(p("POST /vpn/tailscale/login"), s.tsLogin)
	m.HandleFunc(p("POST /vpn/tailscale/logout"), s.tsLogout)
	m.HandleFunc(p("POST /vpn/tailscale/accept-dns"), s.tsAcceptDNS)
	m.HandleFunc(p("POST /vpn/{id}/connect"), s.vpnConnect)
	m.HandleFunc(p("POST /vpn/{id}/disconnect"), s.vpnDisconnect)
	m.HandleFunc(p("GET /monitor"), s.monitor)
	m.HandleFunc(p("GET /monitor/samples"), s.monitorSamples)
	m.HandleFunc(p("POST /monitor/baseline/reset"), s.monitorReset)
	m.HandleFunc(p("POST /monitor/pause"), s.monitorPause)
	m.HandleFunc(p("POST /monitor/resume"), s.monitorResume)
	m.HandleFunc(p("POST /speed"), s.speed)
	m.HandleFunc(p("GET /speed/history"), s.speedHistory)
	m.HandleFunc(p("GET /events"), s.events)
	m.HandleFunc(p("GET /events/stream"), s.eventStream)
	m.HandleFunc(p("GET /diag/lan"), s.diagLAN)
	m.HandleFunc(p("GET /diag/ports"), s.diagPorts)
	m.HandleFunc(p("GET /diag/routes"), s.diagRoutes)
	m.HandleFunc(p("GET /diag/dns"), s.diagDNS)
	m.HandleFunc(p("GET /diag/public-ip"), s.diagPublicIP)
	m.HandleFunc(p("GET /diag/infra"), s.diagInfra)
	m.HandleFunc(p("GET /secrets"), s.secrets)
	m.HandleFunc(p("GET /secrets/{id}"), s.secret)
	m.HandleFunc(p("POST /secrets/{id}"), s.secretAnswer)
	m.HandleFunc(p("POST /secrets/{id}/cancel"), s.secretCancel)
	m.HandleFunc(p("GET /config"), s.config)
	m.HandleFunc(p("PUT /config"), s.configSet)
	m.HandleFunc(p("POST /notify/test"), s.notifyTest)
}

// ServeHTTP routes through the mux but turns its plain-text 404/405 into the
// JSON error shape every other answer uses.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, pattern := s.mux.Handler(r)
	if pattern != "" {
		s.mux.ServeHTTP(w, r) // the mux, not h, binds path values
		return
	}
	rec := &statusRecorder{header: http.Header{}, status: http.StatusNotFound}
	h.ServeHTTP(rec, r)
	if allow := rec.header.Get("Allow"); allow != "" {
		w.Header().Set("Allow", allow)
	}
	if rec.status == http.StatusMethodNotAllowed {
		writeJSON(w, rec.status, ErrorResponse{Error: fmt.Sprintf("api: %s not allowed on %s", r.Method, r.URL.Path), Hint: "see docs/API.md", Code: string(core.KindInvalid)})
		return
	}
	writeJSON(w, rec.status, ErrorResponse{Error: fmt.Sprintf("api: no route %s %s", r.Method, r.URL.Path), Hint: "see docs/API.md", Code: string(core.KindNotFound)})
}

// statusRecorder captures the status the mux's fallback handler would send.
type statusRecorder struct {
	header http.Header
	status int
}

func (r *statusRecorder) Header() http.Header         { return r.header }
func (r *statusRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *statusRecorder) WriteHeader(code int)        { r.status = code }

// --- plumbing ---------------------------------------------------------------------

// StatusFor maps an error to an HTTP status.
func StatusFor(err error) int {
	switch core.KindOf(err) {
	case core.KindPermission:
		return http.StatusForbidden
	case core.KindNotFound:
		return http.StatusNotFound
	case core.KindUnsupported:
		return http.StatusNotImplemented
	case core.KindInvalid:
		return http.StatusBadRequest
	case core.KindConflict:
		return http.StatusConflict
	case core.KindUnavailable:
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return http.StatusInternalServerError
}

// ErrorBody builds the JSON body for err.
func ErrorBody(err error) ErrorResponse {
	kind := core.KindOf(err)
	hint := core.HintOf(err)
	if hint == "" && kind == core.KindPermission {
		hint = "NetworkManager refused this action for your user; a polkit rule or group membership fixes it permanently"
	}
	return ErrorResponse{Error: err.Error(), Hint: hint, Code: string(kind)}
}

func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, StatusFor(err), ErrorBody(err))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func ok(w http.ResponseWriter) { writeJSON(w, http.StatusOK, OKResponse{OK: true}) }

// decode reads a JSON body into v. An empty body is allowed (v stays zero).
func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return core.Wrap(core.KindInvalid, "", err)
	}
	if len(body) > maxBody {
		return core.Errorf(core.KindInvalid, "", "api: body larger than %d bytes", maxBody)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return core.Errorf(core.KindInvalid, "", "api: bad JSON body: %v", err)
	}
	return nil
}

func queryInt(r *http.Request, key string, def int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, core.Errorf(core.KindInvalid, "", "api: %s must be an integer", key)
	}
	return n, nil
}

func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(r.URL.Query().Get(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// nonNil turns a nil slice into an empty one so JSON says [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// --- status / devices / wifi ------------------------------------------------------

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	snap := s.d.Snapshot()
	writeJSON(w, http.StatusOK, StatusResponse{
		Status:          snap.Status,
		Version:         s.d.Version(),
		APIVersion:      version.APIVersion,
		UptimeSeconds:   s.d.Uptime().Seconds(),
		Started:         s.d.Started(),
		SnapshotVersion: snap.Version,
	})
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, nonNil(s.d.Devices()))
}

func (s *Server) wifi(w http.ResponseWriter, r *http.Request) {
	nets, err := s.d.Wifi(r.URL.Query().Get("device"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(nets))
}

func (s *Server) wifiScan(w http.ResponseWriter, r *http.Request) {
	var req DeviceRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.ScanWifi(r.Context(), req.Device); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) wifiConnect(w http.ResponseWriter, r *http.Request) {
	var req core.ConnectWifiRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.ConnectWifi(r.Context(), req); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) wifiDisconnect(w http.ResponseWriter, r *http.Request) {
	var req DeviceRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.DisconnectDevice(r.Context(), req.Device); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) wifiForget(w http.ResponseWriter, r *http.Request) {
	var req UUIDRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.Forget(r.Context(), req.UUID); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) wifiEnabled(w http.ResponseWriter, r *http.Request) {
	var req OnRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.SetWifiEnabled(r.Context(), req.On); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

// --- profiles ------------------------------------------------------------------------

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, nonNil(s.d.Profiles()))
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	p, err := s.d.Profile(r.PathValue("uuid"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) profileIP(w http.ResponseWriter, r *http.Request) {
	var req IPRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.UpdateIPConfig(r.Context(), r.PathValue("uuid"), req.IPv4, req.IPv6); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) profileActivate(w http.ResponseWriter, r *http.Request) {
	var req DeviceRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.Activate(r.Context(), r.PathValue("uuid"), req.Device); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) profileDeactivate(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Deactivate(r.Context(), r.PathValue("uuid")); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) profileAutoconnect(w http.ResponseWriter, r *http.Request) {
	var req OnRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.SetAutoconnect(r.Context(), r.PathValue("uuid"), req.On); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) profileDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.d.DeleteProfile(r.Context(), r.PathValue("uuid")); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) active(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, nonNil(s.d.Active()))
}

// --- vpn -------------------------------------------------------------------------------

func (s *Server) vpns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, nonNil(s.d.VPNs()))
}

func (s *Server) vpnConnect(w http.ResponseWriter, r *http.Request) {
	if err := s.d.ConnectVPN(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) vpnDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.d.DisconnectVPN(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) vpnImport(w http.ResponseWriter, r *http.Request) {
	var req ImportVPNRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.d.ImportVPN(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) tsExitNode(w http.ResponseWriter, r *http.Request) {
	var req ExitNodeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.SetExitNode(r.Context(), req.Peer, req.AllowLAN); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) tsExitNodeEnabled(w http.ResponseWriter, r *http.Request) {
	var req OnRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.UseExitNode(r.Context(), req.On); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) tsLogin(w http.ResponseWriter, r *http.Request) {
	url, err := s.d.TailscaleLogin(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, LoginResponse{URL: url})
}

func (s *Server) tsLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.d.TailscaleLogout(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) tsAcceptDNS(w http.ResponseWriter, r *http.Request) {
	var req OnRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.SetAcceptDNS(r.Context(), req.On); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

// --- monitor ---------------------------------------------------------------------------

func (s *Server) monitor(w http.ResponseWriter, r *http.Request) {
	st := s.d.MonitorStatus()
	st.Anchors = nonNil(st.Anchors)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) monitorSamples(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 200)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	samples, err := s.d.Samples(r.Context(), q.Get("key"), q.Get("anchor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(samples))
}

func (s *Server) monitorReset(w http.ResponseWriter, r *http.Request) {
	var req KeyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.ResetBaseline(r.Context(), req.Key); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) monitorPause(w http.ResponseWriter, r *http.Request) {
	if err := s.d.PauseMonitor(); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) monitorResume(w http.ResponseWriter, r *http.Request) {
	if err := s.d.ResumeMonitor(); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

// --- speed -------------------------------------------------------------------------------

func (s *Server) speed(w http.ResponseWriter, r *http.Request) {
	var opts core.SpeedOptions
	if err := decode(r, &opts); err != nil {
		writeError(w, err)
		return
	}
	if queryBool(r, "wait") {
		res, err := s.d.RunSpeed(r.Context(), opts, nil)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := s.d.RunSpeed(r.Context(), opts, func(p core.SpeedProgress) {
		_ = sse.send(SSEProgress, p)
	})
	if err != nil {
		_ = sse.send(SSEError, ErrorBody(err))
		return
	}
	_ = sse.send(SSEResult, res)
}

func (s *Server) speedHistory(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		writeError(w, err)
		return
	}
	hist, err := s.d.SpeedHistory(r.Context(), r.URL.Query().Get("key"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(hist))
}

// --- events --------------------------------------------------------------------------------

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		writeError(w, err)
		return
	}
	evs, err := s.d.Events(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(evs))
}

func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	// Subscribe before the headers go out: a client that has seen the response
	// must not miss an event published a moment later.
	ch, cancel := s.d.Subscribe()
	defer cancel()
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, err)
		return
	}
	_ = sse.comment("connected")
	hb := time.NewTicker(s.heartbeat)
	defer hb.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if err := sse.comment("ping"); err != nil {
				return
			}
		case item, okc := <-ch:
			if !okc {
				return
			}
			var werr error
			switch {
			case item.Event != nil:
				werr = sse.send(SSEEvent, item.Event)
			case item.Change != nil:
				werr = sse.send(SSEChange, item.Change)
			}
			if werr != nil {
				return
			}
		}
	}
}

// --- diag -------------------------------------------------------------------------------------

func (s *Server) diagLAN(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	if device == "" {
		// Default to the device carrying the primary connection: "devices on my network".
		if p := s.d.Snapshot().Status.Primary; p != nil && len(p.Devices) > 0 {
			device = p.Devices[0]
		}
	}
	if device == "" {
		writeError(w, core.Errorf(core.KindInvalid, "pass ?device=<name> or connect to a network first", "diag: no device given and nothing is connected"))
		return
	}
	hosts, err := s.d.Diag().LANHosts(r.Context(), device, queryBool(r, "sweep"))
	var se *diag.SweepError
	if errors.As(err, &se) {
		// The sweep could not run (no ping socket on this kernel) but the
		// neighbour table is valid: answer with it and say why in a header
		// instead of failing the whole call.
		s.log.Warn("diag: lan sweep skipped", "device", device, "err", err, "hint", core.HintOf(err))
		w.Header().Set("Warning", `199 - "`+strings.ReplaceAll(err.Error(), `"`, `'`)+`"`)
		err = nil
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(hosts))
}

func (s *Server) diagPorts(w http.ResponseWriter, r *http.Request) {
	ports, err := s.d.Diag().ListeningPorts(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(ports))
}

func (s *Server) diagRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.d.Diag().Routes(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(routes))
}

func (s *Server) diagDNS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("name") == "" {
		writeError(w, core.Errorf(core.KindInvalid, "", "api: name is required"))
		return
	}
	ans, err := s.d.Diag().DNSLookup(r.Context(), q.Get("name"), q.Get("server"), q.Get("type"))
	if err != nil {
		writeError(w, err)
		return
	}
	ans.Answers = nonNil(ans.Answers)
	writeJSON(w, http.StatusOK, ans)
}

func (s *Server) diagPublicIP(w http.ResponseWriter, r *http.Request) {
	ip, err := s.d.Diag().PublicIP(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ip)
}

func (s *Server) diagInfra(w http.ResponseWriter, r *http.Request) {
	nets, err := s.d.Diag().Infra(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(nets))
}

// --- secrets ----------------------------------------------------------------------------------

func (s *Server) secrets(w http.ResponseWriter, r *http.Request) {
	reqs, err := s.d.PendingSecrets(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(reqs))
}

func (s *Server) secret(w http.ResponseWriter, r *http.Request) {
	req, err := s.d.Secret(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	req.Fields = nonNil(req.Fields)
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) secretAnswer(w http.ResponseWriter, r *http.Request) {
	var ans core.SecretAnswer
	if err := decode(r, &ans); err != nil {
		writeError(w, err)
		return
	}
	if err := s.d.AnswerSecret(r.Context(), r.PathValue("id"), ans); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

func (s *Server) secretCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.d.CancelSecret(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

// --- config / notify ----------------------------------------------------------------------------

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.d.Config())
}

func (s *Server) configSet(w http.ResponseWriter, r *http.Request) {
	var req ConfigSetRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Key == "" {
		writeError(w, core.Errorf(core.KindInvalid, "", "api: key is required"))
		return
	}
	cfg, err := s.d.SetConfig(req.Key, req.Value)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	if err := s.d.NotifyTest(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	ok(w)
}

// --- SSE ------------------------------------------------------------------------------------------

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, error) {
	f, okf := w.(http.Flusher)
	if !okf {
		return nil, core.Errorf(core.KindInternal, "", "api: response writer cannot stream")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, nil
}

func (s *sseWriter) send(event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}
