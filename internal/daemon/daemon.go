// Package daemon is bnmd's core: it owns the NetworkManager subscription, the
// VPN registry, the monitor, history and the event stream, keeps a versioned
// snapshot of the world, derives Events (connected, disconnected, no-internet,
// internet-restored, vpn-up/down) from state transitions, persists and
// broadcasts them and hands them to the Notifier. It also owns the Unix socket
// and its lock. internal/api serves HTTP over the socket by calling into
// *Daemon.
//
// Backends are ports: core.NetworkManager plus the small VPNRegistry, Monitor
// and Diag interfaces declared here, so the package is tested end to end with
// internal/fake and never needs D-Bus or a network.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/paths"
	"github.com/dopeCape/better-nm/internal/version"
)

// VPNRegistry is what the daemon needs from internal/vpn.Registry.
type VPNRegistry interface {
	List(ctx context.Context) ([]core.VPN, error)
	Connect(ctx context.Context, id string) error
	Disconnect(ctx context.Context, id string) error
	Watch(ctx context.Context) (<-chan core.Change, error)
	// Tailscale returns the Tailscale control surface, or nil when no adapter offers one.
	Tailscale() core.TailscaleControl
}

// Monitor is what the daemon needs from internal/monitor.
type Monitor interface {
	Run(ctx context.Context) error
	SetNetwork(networkKey, gatewayIP string)
	Status() core.MonitorStatus
	Pause()
	Resume()
	ResetBaseline(ctx context.Context, networkKey string) error
	Samples(ctx context.Context, networkKey, anchor string, limit int) ([]core.Sample, error)
	Events() <-chan core.Event
	Changes() <-chan core.Change
}

// Diag is the diagnostics surface; DiagFuncs adapts internal/diag's functions to it.
type Diag interface {
	LANHosts(ctx context.Context, device string, sweep bool) ([]core.LANHost, error)
	ListeningPorts(ctx context.Context) ([]core.ListeningPort, error)
	Routes(ctx context.Context) ([]core.Route, error)
	DNSLookup(ctx context.Context, name, server, qtype string) (core.DNSAnswer, error)
	PublicIP(ctx context.Context) (core.PublicIP, error)
	Infra(ctx context.Context) ([]core.InfraNetwork, error)
}

// DiagFuncs implements Diag from plain functions; a nil field is "unsupported".
type DiagFuncs struct {
	LANHostsFn       func(ctx context.Context, device string, sweep bool) ([]core.LANHost, error)
	ListeningPortsFn func(ctx context.Context) ([]core.ListeningPort, error)
	RoutesFn         func(ctx context.Context) ([]core.Route, error)
	DNSLookupFn      func(ctx context.Context, name, server, qtype string) (core.DNSAnswer, error)
	PublicIPFn       func(ctx context.Context) (core.PublicIP, error)
	InfraFn          func(ctx context.Context) ([]core.InfraNetwork, error)
}

func errDiagUnsupported(what string) error {
	return core.Errorf(core.KindUnsupported, "", "diag: %s is not available in this build", what)
}

func (f DiagFuncs) LANHosts(ctx context.Context, device string, sweep bool) ([]core.LANHost, error) {
	if f.LANHostsFn == nil {
		return nil, errDiagUnsupported("lan")
	}
	return f.LANHostsFn(ctx, device, sweep)
}

func (f DiagFuncs) ListeningPorts(ctx context.Context) ([]core.ListeningPort, error) {
	if f.ListeningPortsFn == nil {
		return nil, errDiagUnsupported("ports")
	}
	return f.ListeningPortsFn(ctx)
}

func (f DiagFuncs) Routes(ctx context.Context) ([]core.Route, error) {
	if f.RoutesFn == nil {
		return nil, errDiagUnsupported("routes")
	}
	return f.RoutesFn(ctx)
}

func (f DiagFuncs) DNSLookup(ctx context.Context, name, server, qtype string) (core.DNSAnswer, error) {
	if f.DNSLookupFn == nil {
		return core.DNSAnswer{}, errDiagUnsupported("dns")
	}
	return f.DNSLookupFn(ctx, name, server, qtype)
}

func (f DiagFuncs) PublicIP(ctx context.Context) (core.PublicIP, error) {
	if f.PublicIPFn == nil {
		return core.PublicIP{}, errDiagUnsupported("public-ip")
	}
	return f.PublicIPFn(ctx)
}

func (f DiagFuncs) Infra(ctx context.Context) ([]core.InfraNetwork, error) {
	if f.InfraFn == nil {
		return nil, errDiagUnsupported("infra")
	}
	return f.InfraFn(ctx)
}

// WireGuardImporter is what internal/vpn/wireguard offers for .conf import.
type WireGuardImporter interface {
	Import(ctx context.Context, name string, conf io.Reader) (uuid string, err error)
}

// PolicyUpdater is implemented by a Notifier that can take a new policy at runtime.
type PolicyUpdater interface {
	SetPolicy(config.Notify)
}

// SecretSource is implemented by a core.SecretBroker that originates requests
// (the NM secret agent, the fake): the daemon installs the callbacks that turn
// a new request into a secret-needed event and its resolution into
// secret-resolved.
type SecretSource interface {
	SetSecretHandler(func(core.SecretRequest))
	SetSecretResolvedHandler(func(id string, outcome core.SecretOutcome))
}

// Options wires the daemon. NM, Store and Config are required; the rest may be
// nil and the matching routes answer 501.
type Options struct {
	NM        core.NetworkManager
	VPN       VPNRegistry
	Monitor   Monitor
	Store     core.Store
	Speed     core.SpeedTester
	Notifier  core.Notifier
	Diag      Diag
	WireGuard WireGuardImporter
	// Secrets answers NetworkManager's secret requests (the nm.Client is the
	// broker); nil makes the /secrets routes answer 501.
	Secrets core.SecretBroker
	Config  config.Config
	// ConfigPath is where PUT /config persists; "" = config.Path().
	ConfigPath string
	Logger     *slog.Logger
	Version    string

	// Debounce and ConnectivityGrace override the deriver's timers (tests);
	// 0 = default, negative = none.
	Debounce          time.Duration
	ConnectivityGrace time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
	// PruneInterval is how often Store.Prune runs; 0 = hourly.
	PruneInterval time.Duration
}

// Snapshot is the cached state of the world, bumped on every change.
type Snapshot struct {
	Version   uint64                  `json:"version"`
	UpdatedAt time.Time               `json:"updated_at"`
	Status    core.Status             `json:"status"`
	Devices   []core.Device           `json:"devices"`
	Wifi      []core.WifiNetwork      `json:"wifi"`
	Profiles  []core.Profile          `json:"profiles"`
	Active    []core.ActiveConnection `json:"active"`
	VPNs      []core.VPN              `json:"vpns"`
	Monitor   core.MonitorStatus      `json:"monitor"`
}

// Daemon is the running service.
type Daemon struct {
	o       Options
	log     *slog.Logger
	now     func() time.Time
	started time.Time

	snapMu sync.RWMutex
	snap   Snapshot

	// derMu serialises refresh+derive between the run loop and API mutations.
	derMu    sync.Mutex
	der      *deriver
	lastNet  [2]string
	netSet   bool
	notifyCh chan core.Event
	bus      *bus

	speedMu      sync.Mutex
	speedRunning bool

	cfgMu sync.RWMutex
	cfg   config.Config

	runMu   sync.Mutex
	running bool
	wake    chan struct{}
}

// New validates options and builds a Daemon; Run starts it.
func New(o Options) (*Daemon, error) {
	if o.NM == nil {
		return nil, errors.New("daemon: NM is required")
	}
	if o.Store == nil {
		return nil, errors.New("daemon: Store is required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Version == "" {
		o.Version = version.Version
	}
	if o.ConfigPath == "" {
		o.ConfigPath = config.Path()
	}
	deb := o.Debounce
	if deb == 0 {
		deb = DefaultDebounce
	}
	grace := o.ConnectivityGrace
	if grace == 0 {
		grace = DefaultConnectivityGrace
	}
	d := &Daemon{
		o:        o,
		log:      o.Logger,
		now:      o.Now,
		started:  o.Now(),
		der:      newDeriver(deb, grace),
		notifyCh: make(chan core.Event, 64),
		bus:      newBus(),
		cfg:      o.Config,
		wake:     make(chan struct{}, 1),
	}
	if src, ok := o.Secrets.(SecretSource); ok {
		src.SetSecretHandler(d.onSecretNeeded)
		src.SetSecretResolvedHandler(d.onSecretResolved)
	}
	return d, nil
}

// Version is the daemon's build version.
func (d *Daemon) Version() string { return d.o.Version }

// Started is when the daemon was constructed.
func (d *Daemon) Started() time.Time { return d.started }

// Uptime is how long the daemon has been up.
func (d *Daemon) Uptime() time.Duration { return d.now().Sub(d.started) }

// Snapshot returns a copy of the cached world.
func (d *Daemon) Snapshot() Snapshot {
	d.snapMu.RLock()
	defer d.snapMu.RUnlock()
	return d.snap
}

// Subscribe attaches to the event stream. Call cancel to detach.
func (d *Daemon) Subscribe() (<-chan StreamItem, func()) {
	s, cancel := d.bus.subscribe()
	return s.ch, cancel
}

// Subscribers is the number of attached stream consumers.
func (d *Daemon) Subscribers() int { return d.bus.count() }

// Run reads the world, subscribes to every backend and serves the event loop
// until ctx ends. It returns nil on a clean shutdown.
func (d *Daemon) Run(ctx context.Context) error {
	d.runMu.Lock()
	if d.running {
		d.runMu.Unlock()
		return errors.New("daemon: already running")
	}
	d.running = true
	d.runMu.Unlock()
	defer func() {
		d.runMu.Lock()
		d.running = false
		d.runMu.Unlock()
	}()

	if err := d.initialRead(ctx); err != nil {
		return err
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	nmCh, err := d.o.NM.Watch(loopCtx)
	if err != nil {
		return fmt.Errorf("daemon: watch NM: %w", err)
	}
	var vpnCh <-chan core.Change
	if d.o.VPN != nil {
		vpnCh, err = d.o.VPN.Watch(loopCtx)
		if err != nil {
			d.log.Warn("vpn watch unavailable", "err", err)
		}
	}
	var monEv <-chan core.Event
	var monCh <-chan core.Change
	if d.o.Monitor != nil {
		monEv, monCh = d.o.Monitor.Events(), d.o.Monitor.Changes()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.o.Monitor.Run(loopCtx); err != nil && !errors.Is(err, context.Canceled) {
				d.log.Error("monitor stopped", "err", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.notifyLoop(loopCtx)
	}()

	prune := d.o.PruneInterval
	if prune == 0 {
		prune = time.Hour
	}
	pruneT := time.NewTicker(prune)
	defer pruneT.Stop()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	d.armTimer(timer)

	d.log.Info("bnmd running", "version", d.o.Version, "api", version.APIVersion)
	for {
		select {
		case <-ctx.Done():
			d.log.Info("bnmd stopping")
			return nil
		case c, ok := <-nmCh:
			if !ok {
				d.log.Error("NM watch closed; state will go stale until restart")
				nmCh = nil
				continue
			}
			changes := append([]core.Change{c}, drainChanges(nmCh)...)
			d.onNMChanges(ctx, changes)
			d.armTimer(timer)
		case c, ok := <-vpnCh:
			if !ok {
				d.log.Warn("VPN watch closed")
				vpnCh = nil
				continue
			}
			changes := append([]core.Change{c}, drainChanges(vpnCh)...)
			d.onVPNChanges(ctx, changes)
			d.armTimer(timer)
		case c, ok := <-monCh:
			if !ok {
				monCh = nil
				continue
			}
			d.onMonitorChange(c)
		case e, ok := <-monEv:
			if !ok {
				monEv = nil
				continue
			}
			d.onMonitorEvent(ctx, e)
		case <-timer.C:
			d.derMu.Lock()
			evs := d.der.flush(d.now())
			d.syncMonitorNetwork()
			d.derMu.Unlock()
			d.emitEvents(ctx, evs)
			d.armTimer(timer)
		case <-d.wake:
			d.armTimer(timer)
		case <-pruneT.C:
			if err := d.o.Store.Prune(ctx); err != nil {
				d.log.Warn("prune failed", "err", err)
			}
		}
	}
}

func drainChanges(ch <-chan core.Change) []core.Change {
	var out []core.Change
	for i := 0; i < 64; i++ {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
		default:
			return out
		}
	}
	return out
}

func (d *Daemon) armTimer(t *time.Timer) {
	d.derMu.Lock()
	deadline, ok := d.der.nextDeadline()
	d.derMu.Unlock()
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	if ok {
		wait := deadline.Sub(d.now())
		if wait < 0 {
			wait = 0
		}
		t.Reset(wait)
	}
}

func (d *Daemon) initialRead(ctx context.Context) error {
	d.derMu.Lock()
	defer d.derMu.Unlock()
	if err := d.refreshNM(ctx, true); err != nil {
		return fmt.Errorf("daemon: initial read: %w", err)
	}
	d.refreshVPN(ctx)
	d.refreshMonitor()
	evs := d.der.apply(d.view(), d.now())
	d.syncMonitorNetwork()
	d.emitEvents(ctx, evs)
	return nil
}

// view returns what the deriver needs from the snapshot.
func (d *Daemon) view() view {
	s := d.Snapshot()
	return view{Status: s.Status, Profiles: s.Profiles, VPNs: s.VPNs}
}

func (d *Daemon) onNMChanges(ctx context.Context, changes []core.Change) {
	needWifi := false
	for _, c := range changes {
		if c.Kind == core.ChangeWifi || c.Kind == core.ChangeDevices || c.Kind == core.ChangeStatus {
			needWifi = true
		}
	}
	d.derMu.Lock()
	if err := d.refreshNM(ctx, needWifi); err != nil {
		d.log.Warn("refresh after NM change failed", "err", err)
	}
	evs := d.der.apply(d.view(), d.now())
	d.syncMonitorNetwork()
	d.derMu.Unlock()
	for _, c := range changes {
		c := c
		d.bus.publish(StreamItem{Change: &c})
	}
	d.emitEvents(ctx, evs)
}

func (d *Daemon) onVPNChanges(ctx context.Context, changes []core.Change) {
	d.derMu.Lock()
	d.refreshVPN(ctx)
	evs := d.der.apply(d.view(), d.now())
	d.derMu.Unlock()
	for _, c := range changes {
		c := c
		if c.Kind == "" {
			c.Kind = core.ChangeVPN
		}
		d.bus.publish(StreamItem{Change: &c})
	}
	d.emitEvents(ctx, evs)
}

func (d *Daemon) onMonitorChange(c core.Change) {
	d.refreshMonitor()
	if c.Kind == "" {
		c.Kind = core.ChangeMonitor
	}
	d.bus.publish(StreamItem{Change: &c})
}

func (d *Daemon) onMonitorEvent(ctx context.Context, e core.Event) {
	if e.Time.IsZero() {
		e.Time = d.now()
	}
	if e.NetworkKey == "" {
		e.NetworkKey = d.Snapshot().Status.NetworkKey
	}
	if e.Title == "" {
		switch e.Type {
		case core.EventDegraded:
			e.Title = "Network degraded"
		case core.EventRecovered:
			e.Title = "Network recovered"
		default:
			e.Title = string(e.Type)
		}
	}
	if e.Urgency == "" {
		if e.Type == core.EventDegraded {
			e.Urgency = "normal"
		} else {
			e.Urgency = "low"
		}
	}
	d.refreshMonitor()
	d.emitEvents(ctx, []core.Event{e})
}

// refreshNM re-reads NM into the snapshot. Caller holds derMu.
func (d *Daemon) refreshNM(ctx context.Context, wifi bool) error {
	nm := d.o.NM
	status, err := nm.Status(ctx)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	devices, err := nm.Devices(ctx)
	if err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	profiles, err := nm.Profiles(ctx)
	if err != nil {
		return fmt.Errorf("profiles: %w", err)
	}
	active, err := nm.ActiveConnections(ctx)
	if err != nil {
		return fmt.Errorf("active: %w", err)
	}
	var nets []core.WifiNetwork
	if wifi {
		nets, err = nm.WifiNetworks(ctx, "")
		if err != nil {
			d.log.Debug("wifi list failed", "err", err)
			nets = nil
		}
	}
	if status.NetworkKey == "" && status.Primary != nil {
		status.NetworkKey = core.NetworkKey(status.Primary, profiles)
	}
	d.snapMu.Lock()
	d.snap.Status = status
	d.snap.Devices = devices
	d.snap.Profiles = profiles
	d.snap.Active = active
	if wifi {
		d.snap.Wifi = nets
	}
	d.bump()
	d.snapMu.Unlock()
	return nil
}

// refreshVPN re-reads the VPN list. Caller holds derMu.
func (d *Daemon) refreshVPN(ctx context.Context) {
	if d.o.VPN == nil {
		return
	}
	vpns, err := d.o.VPN.List(ctx)
	if err != nil {
		d.log.Warn("vpn list failed", "err", err)
		return
	}
	d.snapMu.Lock()
	d.snap.VPNs = vpns
	d.bump()
	d.snapMu.Unlock()
}

func (d *Daemon) refreshMonitor() {
	if d.o.Monitor == nil {
		return
	}
	st := d.o.Monitor.Status()
	d.snapMu.Lock()
	d.snap.Monitor = st
	d.bump()
	d.snapMu.Unlock()
}

// bump increments the snapshot version. Caller holds snapMu.
func (d *Daemon) bump() {
	d.snap.Version++
	d.snap.UpdatedAt = d.now()
}

// syncMonitorNetwork tells the monitor which network to probe when the
// deriver's view of it changed. Caller holds derMu.
func (d *Daemon) syncMonitorNetwork() {
	if d.o.Monitor == nil {
		return
	}
	key, gw := d.der.monitorNetwork()
	if d.netSet && d.lastNet == [2]string{key, gw} {
		return
	}
	d.netSet = true
	d.lastNet = [2]string{key, gw}
	d.o.Monitor.SetNetwork(key, gw)
}

// emitEvents persists, broadcasts and queues events for notification.
func (d *Daemon) emitEvents(ctx context.Context, evs []core.Event) {
	if len(evs) == 0 {
		return
	}
	// ctx may be an API request's: the event is a fact about the world and
	// belongs in history even when that client has gone away, so the store
	// write is bounded on its own rather than cancelled with the request.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, e := range evs {
		e := e
		if err := d.o.Store.AddEvent(sctx, e); err != nil {
			d.log.Warn("store event failed", "type", e.Type, "err", err)
		}
		d.log.Info("event", "type", e.Type, "title", e.Title, "network", e.NetworkKey)
		d.bus.publish(StreamItem{Event: &e})
		if d.o.Notifier != nil {
			select {
			case d.notifyCh <- e:
			default:
				d.log.Warn("notifier queue full; dropping", "type", e.Type)
			}
		}
	}
}

func (d *Daemon) notifyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-d.notifyCh:
			nctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := d.o.Notifier.Notify(nctx, e); err != nil {
				d.log.Warn("notify failed", "type", e.Type, "err", err)
			}
			cancel()
		}
	}
}

// mutate runs a backend write, then refreshes the snapshot and derives events
// synchronously so the caller's next read sees the new world. The run loop will
// also see the backend's own change hints; deriving twice is harmless.
func (d *Daemon) mutate(ctx context.Context, fn func() error) error {
	if err := fn(); err != nil {
		return err
	}
	d.Refresh(ctx)
	return nil
}

// Refresh re-reads every backend now (also called after external mutations).
func (d *Daemon) Refresh(ctx context.Context) {
	d.derMu.Lock()
	if err := d.refreshNM(ctx, true); err != nil {
		d.log.Warn("refresh failed", "err", err)
	}
	d.refreshVPN(ctx)
	d.refreshMonitor()
	evs := d.der.apply(d.view(), d.now())
	d.syncMonitorNetwork()
	d.derMu.Unlock()
	d.emitEvents(ctx, evs)
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// --- reads served from the snapshot -------------------------------------------

// Status is the cached core.Status.
func (d *Daemon) Status() core.Status { return d.Snapshot().Status }

// Devices lists cached devices.
func (d *Daemon) Devices() []core.Device { return d.Snapshot().Devices }

// Wifi lists cached Wi-Fi networks, optionally for one device.
func (d *Daemon) Wifi(device string) ([]core.WifiNetwork, error) {
	s := d.Snapshot()
	if device == "" {
		return s.Wifi, nil
	}
	found := false
	for _, dev := range s.Devices {
		if dev.Name == device {
			found = true
			if dev.Kind != core.DeviceWifi {
				return nil, core.Errorf(core.KindInvalid, "", "daemon: %s is not a Wi-Fi device", device)
			}
		}
	}
	if !found {
		return nil, core.Errorf(core.KindNotFound, "run `bnm devices`", "daemon: device %q not found", device)
	}
	out := []core.WifiNetwork{}
	for _, w := range s.Wifi {
		if w.Device == device {
			out = append(out, w)
		}
	}
	return out, nil
}

// Profiles lists cached profiles.
func (d *Daemon) Profiles() []core.Profile { return d.Snapshot().Profiles }

// Profile finds one cached profile by UUID.
func (d *Daemon) Profile(uuid string) (core.Profile, error) {
	for _, p := range d.Snapshot().Profiles {
		if p.UUID == uuid {
			return p, nil
		}
	}
	return core.Profile{}, core.Errorf(core.KindNotFound, "run `bnm profiles`", "daemon: profile %s not found", uuid)
}

// Active lists cached active connections.
func (d *Daemon) Active() []core.ActiveConnection { return d.Snapshot().Active }

// VPNs lists cached VPNs.
func (d *Daemon) VPNs() []core.VPN { return d.Snapshot().VPNs }

// MonitorStatus is the cached monitor summary.
func (d *Daemon) MonitorStatus() core.MonitorStatus { return d.Snapshot().Monitor }

// --- Wi-Fi ----------------------------------------------------------------------

func (d *Daemon) ScanWifi(ctx context.Context, device string) error {
	return d.mutate(ctx, func() error { return d.o.NM.Scan(ctx, device) })
}

func (d *Daemon) ConnectWifi(ctx context.Context, req core.ConnectWifiRequest) error {
	if req.SSID == "" {
		return core.Errorf(core.KindInvalid, "", "daemon: ssid is required")
	}
	return d.mutate(ctx, func() error { return d.o.NM.ConnectWifi(ctx, req) })
}

func (d *Daemon) DisconnectDevice(ctx context.Context, device string) error {
	if device == "" {
		for _, dev := range d.Snapshot().Devices {
			if dev.Kind == core.DeviceWifi {
				device = dev.Name
				break
			}
		}
		if device == "" {
			return core.Errorf(core.KindNotFound, "", "daemon: no Wi-Fi device")
		}
	}
	return d.mutate(ctx, func() error { return d.o.NM.DisconnectDevice(ctx, device) })
}

func (d *Daemon) Forget(ctx context.Context, uuid string) error {
	if uuid == "" {
		return core.Errorf(core.KindInvalid, "", "daemon: uuid is required")
	}
	return d.mutate(ctx, func() error { return d.o.NM.Forget(ctx, uuid) })
}

func (d *Daemon) SetWifiEnabled(ctx context.Context, on bool) error {
	return d.mutate(ctx, func() error { return d.o.NM.SetWifiEnabled(ctx, on) })
}

// --- profiles -------------------------------------------------------------------

func (d *Daemon) UpdateIPConfig(ctx context.Context, uuid string, ipv4, ipv6 *core.IPConfig) error {
	if ipv4 == nil && ipv6 == nil {
		return core.Errorf(core.KindInvalid, "", "daemon: ipv4 or ipv6 is required")
	}
	return d.mutate(ctx, func() error { return d.o.NM.UpdateIPConfig(ctx, uuid, ipv4, ipv6) })
}

func (d *Daemon) Activate(ctx context.Context, uuid, device string) error {
	return d.mutate(ctx, func() error { return d.o.NM.Activate(ctx, uuid, device) })
}

func (d *Daemon) Deactivate(ctx context.Context, uuid string) error {
	return d.mutate(ctx, func() error { return d.o.NM.Deactivate(ctx, uuid) })
}

func (d *Daemon) SetAutoconnect(ctx context.Context, uuid string, on bool) error {
	return d.mutate(ctx, func() error { return d.o.NM.SetAutoconnect(ctx, uuid, on) })
}

func (d *Daemon) DeleteProfile(ctx context.Context, uuid string) error {
	return d.mutate(ctx, func() error { return d.o.NM.Forget(ctx, uuid) })
}

// --- VPN --------------------------------------------------------------------------

func (d *Daemon) vpn() (VPNRegistry, error) {
	if d.o.VPN == nil {
		return nil, core.Errorf(core.KindUnsupported, "", "daemon: no VPN backends in this build")
	}
	return d.o.VPN, nil
}

func (d *Daemon) ConnectVPN(ctx context.Context, id string) error {
	r, err := d.vpn()
	if err != nil {
		return err
	}
	return d.mutate(ctx, func() error { return r.Connect(ctx, id) })
}

func (d *Daemon) DisconnectVPN(ctx context.Context, id string) error {
	r, err := d.vpn()
	if err != nil {
		return err
	}
	return d.mutate(ctx, func() error { return r.Disconnect(ctx, id) })
}

// ImportVPNRequest imports a WireGuard .conf or an OpenVPN .ovpn, from a path
// on the daemon's host or from inline content.
type ImportVPNRequest struct {
	Kind    string `json:"kind"` // wireguard | openvpn
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
}

// ImportVPNResult names the imported profile.
type ImportVPNResult struct {
	ID   string    `json:"id"`
	UUID string    `json:"uuid"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
	VPN  *core.VPN `json:"vpn,omitempty"`
}

func (d *Daemon) ImportVPN(ctx context.Context, req ImportVPNRequest) (ImportVPNResult, error) {
	if req.Path == "" && req.Content == "" {
		return ImportVPNResult{}, core.Errorf(core.KindInvalid, "", "daemon: path or content is required")
	}
	if req.Path != "" && req.Content != "" {
		return ImportVPNResult{}, core.Errorf(core.KindInvalid, "", "daemon: give path or content, not both")
	}
	name := req.Name
	if name == "" && req.Path != "" {
		name = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(req.Path), ".conf"), ".ovpn")
	}
	var uuid string
	switch strings.ToLower(req.Kind) {
	case "wireguard", "wg":
		if d.o.WireGuard == nil {
			return ImportVPNResult{}, core.Errorf(core.KindUnsupported, "", "daemon: WireGuard import is not wired in this build")
		}
		if name == "" {
			return ImportVPNResult{}, core.Errorf(core.KindInvalid, "", "daemon: name is required for inline WireGuard content")
		}
		var r io.Reader
		if req.Content != "" {
			r = strings.NewReader(req.Content)
		} else {
			f, err := os.Open(req.Path)
			if err != nil {
				return ImportVPNResult{}, core.Wrap(core.KindNotFound, "", fmt.Errorf("daemon: open %s: %w", req.Path, err))
			}
			defer f.Close()
			r = f
		}
		var err error
		err = d.mutate(ctx, func() error {
			uuid, err = d.o.WireGuard.Import(ctx, name, r)
			return err
		})
		if err != nil {
			return ImportVPNResult{}, err
		}
	case "openvpn", "ovpn":
		path := req.Path
		if req.Content != "" {
			if name == "" {
				name = "imported"
			}
			dir := filepath.Join(paths.StateDir(), "imports")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return ImportVPNResult{}, fmt.Errorf("daemon: import dir: %w", err)
			}
			f, err := os.CreateTemp(dir, name+"-*.ovpn")
			if err != nil {
				return ImportVPNResult{}, fmt.Errorf("daemon: temp file: %w", err)
			}
			path = f.Name()
			if _, err := f.WriteString(req.Content); err != nil {
				f.Close()
				os.Remove(path)
				return ImportVPNResult{}, fmt.Errorf("daemon: write: %w", err)
			}
			f.Close()
			defer os.Remove(path)
		} else if _, err := os.Stat(path); err != nil {
			return ImportVPNResult{}, core.Wrap(core.KindNotFound, "", fmt.Errorf("daemon: %s: %w", path, err))
		}
		var err error
		err = d.mutate(ctx, func() error {
			uuid, err = d.o.NM.ImportVPN(ctx, "openvpn", path)
			return err
		})
		if err != nil {
			return ImportVPNResult{}, err
		}
	default:
		return ImportVPNResult{}, core.Errorf(core.KindInvalid, "", "daemon: kind must be wireguard or openvpn, got %q", req.Kind)
	}
	res := ImportVPNResult{ID: uuid, UUID: uuid, Name: name, Kind: strings.ToLower(req.Kind)}
	if p, err := d.Profile(uuid); err == nil {
		res.Name = p.Name
	}
	for _, v := range d.VPNs() {
		if v.ID == uuid {
			v := v
			res.VPN = &v
		}
	}
	return res, nil
}

func (d *Daemon) tailscale() (core.TailscaleControl, error) {
	if d.o.VPN == nil {
		return nil, core.Errorf(core.KindUnsupported, "", "daemon: no VPN backends in this build")
	}
	tc := d.o.VPN.Tailscale()
	if tc == nil {
		return nil, core.Errorf(core.KindUnsupported, "install tailscale and start tailscaled", "daemon: Tailscale is not available")
	}
	return tc, nil
}

func (d *Daemon) SetExitNode(ctx context.Context, peer string, allowLAN bool) error {
	tc, err := d.tailscale()
	if err != nil {
		return err
	}
	return d.mutate(ctx, func() error { return tc.SetExitNode(ctx, peer, allowLAN) })
}

func (d *Daemon) UseExitNode(ctx context.Context, on bool) error {
	tc, err := d.tailscale()
	if err != nil {
		return err
	}
	return d.mutate(ctx, func() error { return tc.UseExitNode(ctx, on) })
}

func (d *Daemon) TailscaleLogin(ctx context.Context) (string, error) {
	tc, err := d.tailscale()
	if err != nil {
		return "", err
	}
	var url string
	err = d.mutate(ctx, func() error {
		url, err = tc.Login(ctx)
		return err
	})
	return url, err
}

func (d *Daemon) TailscaleLogout(ctx context.Context) error {
	tc, err := d.tailscale()
	if err != nil {
		return err
	}
	return d.mutate(ctx, func() error { return tc.Logout(ctx) })
}

func (d *Daemon) SetAcceptDNS(ctx context.Context, on bool) error {
	tc, err := d.tailscale()
	if err != nil {
		return err
	}
	return d.mutate(ctx, func() error { return tc.SetAcceptDNS(ctx, on) })
}

// --- monitor ----------------------------------------------------------------------

func (d *Daemon) monitor() (Monitor, error) {
	if d.o.Monitor == nil {
		return nil, core.Errorf(core.KindUnsupported, "", "daemon: monitor is not wired in this build")
	}
	return d.o.Monitor, nil
}

func (d *Daemon) Samples(ctx context.Context, key, anchor string, limit int) ([]core.Sample, error) {
	if key == "" {
		key = d.Snapshot().Monitor.NetworkKey
	}
	if key == "" {
		key = d.Snapshot().Status.NetworkKey
	}
	if m, err := d.monitor(); err == nil {
		return m.Samples(ctx, key, anchor, limit)
	}
	return d.o.Store.Samples(ctx, key, anchor, limit)
}

func (d *Daemon) ResetBaseline(ctx context.Context, key string) error {
	m, err := d.monitor()
	if err != nil {
		return err
	}
	if key == "" {
		key = d.Snapshot().Status.NetworkKey
	}
	if key == "" {
		return core.Errorf(core.KindInvalid, "", "daemon: no network key given and nothing is connected")
	}
	if err := m.ResetBaseline(ctx, key); err != nil {
		return err
	}
	d.refreshMonitor()
	d.bus.publish(StreamItem{Change: &core.Change{Kind: core.ChangeMonitor, Path: key}})
	return nil
}

func (d *Daemon) PauseMonitor() error {
	m, err := d.monitor()
	if err != nil {
		return err
	}
	m.Pause()
	d.refreshMonitor()
	return nil
}

func (d *Daemon) ResumeMonitor() error {
	m, err := d.monitor()
	if err != nil {
		return err
	}
	m.Resume()
	d.refreshMonitor()
	return nil
}

// --- speed --------------------------------------------------------------------------

// RunSpeed runs one speed test, streaming progress, storing the result. Only
// one test runs at a time.
func (d *Daemon) RunSpeed(ctx context.Context, opts core.SpeedOptions, progress func(core.SpeedProgress)) (core.SpeedResult, error) {
	if d.o.Speed == nil {
		return core.SpeedResult{}, core.Errorf(core.KindUnsupported, "", "daemon: speed test is not wired in this build")
	}
	d.speedMu.Lock()
	if d.speedRunning {
		d.speedMu.Unlock()
		return core.SpeedResult{}, core.Errorf(core.KindConflict, "wait for it to finish", "daemon: a speed test is already running")
	}
	d.speedRunning = true
	d.speedMu.Unlock()
	defer func() {
		d.speedMu.Lock()
		d.speedRunning = false
		d.speedMu.Unlock()
	}()

	cfg := d.Config()
	if opts.Provider == "" {
		opts.Provider = cfg.Speed.Provider
	}
	if opts.Server == "" && opts.Provider == "iperf3" {
		opts.Server = cfg.Speed.Iperf3Server
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = cfg.Speed.MaxBytes
	}
	if opts.NetworkKey == "" {
		opts.NetworkKey = d.Snapshot().Status.NetworkKey
	}
	res, err := d.o.Speed.Run(ctx, opts, progress)
	if err != nil {
		return core.SpeedResult{}, err
	}
	if res.Time.IsZero() {
		res.Time = d.now()
	}
	if res.NetworkKey == "" {
		res.NetworkKey = opts.NetworkKey
	}
	if err := d.o.Store.AddSpeedResult(ctx, res); err != nil {
		d.log.Warn("store speed result failed", "err", err)
	}
	return res, nil
}

// SpeedHistory lists stored results, oldest first.
func (d *Daemon) SpeedHistory(ctx context.Context, networkKey string, limit int) ([]core.SpeedResult, error) {
	return d.o.Store.SpeedResults(ctx, networkKey, limit)
}

// Events lists stored events, oldest first.
func (d *Daemon) Events(ctx context.Context, limit int) ([]core.Event, error) {
	return d.o.Store.Events(ctx, limit)
}

// --- diag -----------------------------------------------------------------------------

// Diag returns the diagnostics surface (never nil; unsupported when not wired).
func (d *Daemon) Diag() Diag {
	if d.o.Diag == nil {
		return DiagFuncs{}
	}
	return d.o.Diag
}

// --- config ----------------------------------------------------------------------------

// Config returns the current configuration.
func (d *Daemon) Config() config.Config {
	d.cfgMu.RLock()
	defer d.cfgMu.RUnlock()
	return d.cfg
}

// SetConfig sets one dotted key, persists the file and applies what can be
// applied live (the notification policy). Returns the new configuration.
func (d *Daemon) SetConfig(key, value string) (config.Config, error) {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	next := d.cfg
	next.Monitor.Anchors = append([]string(nil), d.cfg.Monitor.Anchors...)
	next.Notify.MutedNetworks = append([]string(nil), d.cfg.Notify.MutedNetworks...)
	if err := next.Set(key, value); err != nil {
		return d.cfg, err
	}
	if err := next.SaveTo(d.o.ConfigPath); err != nil {
		return d.cfg, err
	}
	d.cfg = next
	if pu, ok := d.o.Notifier.(PolicyUpdater); ok {
		pu.SetPolicy(next.Notify)
	}
	d.log.Info("config updated", "key", key, "value", value)
	return next, nil
}

// --- secrets -----------------------------------------------------------------------------

func (d *Daemon) secrets() (core.SecretBroker, error) {
	if d.o.Secrets == nil {
		return nil, core.Errorf(core.KindUnsupported, "", "daemon: no secret agent in this build")
	}
	return d.o.Secrets, nil
}

// PendingSecrets lists the open secret requests.
func (d *Daemon) PendingSecrets(ctx context.Context) ([]core.SecretRequest, error) {
	b, err := d.secrets()
	if err != nil {
		return nil, err
	}
	return b.Pending(ctx)
}

// Secret finds one open request by id.
func (d *Daemon) Secret(ctx context.Context, id string) (core.SecretRequest, error) {
	reqs, err := d.PendingSecrets(ctx)
	if err != nil {
		return core.SecretRequest{}, err
	}
	for _, r := range reqs {
		if r.ID == id {
			return r, nil
		}
	}
	return core.SecretRequest{}, core.Errorf(core.KindNotFound, "run `bnm secrets`", "daemon: no pending secret request %s", id)
}

// AnswerSecret hands a surface's answer to the broker.
func (d *Daemon) AnswerSecret(ctx context.Context, id string, a core.SecretAnswer) error {
	b, err := d.secrets()
	if err != nil {
		return err
	}
	if id == "" {
		return core.Errorf(core.KindInvalid, "", "daemon: request id is required")
	}
	if len(a.Secrets) == 0 {
		return core.Errorf(core.KindInvalid, "", "daemon: answer has no secrets")
	}
	return b.Answer(ctx, id, a)
}

// CancelSecret cancels a pending request.
func (d *Daemon) CancelSecret(ctx context.Context, id string) error {
	b, err := d.secrets()
	if err != nil {
		return err
	}
	if id == "" {
		return core.Errorf(core.KindInvalid, "", "daemon: request id is required")
	}
	return b.Cancel(ctx, id)
}

// onSecretNeeded is the broker's callback for a new request: it becomes a
// secret-needed event (stored, streamed and notified) whose Data names the
// request so a surface can fetch and answer it.
func (d *Daemon) onSecretNeeded(req core.SecretRequest) {
	name := req.ConnectionName
	if name == "" {
		name = req.SSID
	}
	if name == "" {
		name = req.ConnectionUUID
	}
	e := core.Event{
		Time:       d.now(),
		Type:       core.EventSecretNeeded,
		NetworkKey: secretNetworkKey(req),
		Title:      "Password needed for " + name,
		Body:       secretBody(req),
		Urgency:    "normal",
		Data: map[string]string{
			"request_id":      req.ID,
			"connection_uuid": req.ConnectionUUID,
			"connection_name": req.ConnectionName,
		},
	}
	if req.SSID != "" {
		e.Data["ssid"] = req.SSID
	}
	if req.VPN {
		e.Data["vpn"] = "true"
	}
	d.emitEvents(context.Background(), []core.Event{e})
}

// onSecretResolved is the broker's callback when a request ends.
func (d *Daemon) onSecretResolved(id string, outcome core.SecretOutcome) {
	title := "Password prompt " + string(outcome)
	if outcome == core.SecretTimeout {
		title = "Password prompt timed out"
	}
	e := core.Event{
		Time:    d.now(),
		Type:    core.EventSecretResolved,
		Title:   title,
		Urgency: "low",
		Data:    map[string]string{"request_id": id, "outcome": string(outcome)},
	}
	d.emitEvents(context.Background(), []core.Event{e})
}

// secretNetworkKey files the event under the network it concerns.
func secretNetworkKey(req core.SecretRequest) string {
	switch {
	case req.SSID != "":
		return "wifi:" + req.SSID
	case req.VPN && req.ConnectionUUID != "":
		return "vpn:" + req.ConnectionUUID
	case req.ConnectionUUID != "":
		return "conn:" + req.ConnectionUUID
	}
	return ""
}

// secretBody says what is being asked and how to answer it from a shell.
func secretBody(req core.SecretRequest) string {
	labels := make([]string, 0, len(req.Fields))
	for _, f := range req.Fields {
		labels = append(labels, f.Label)
	}
	what := strings.Join(labels, ", ")
	if what == "" {
		what = "Password"
	}
	var b strings.Builder
	b.WriteString(what)
	switch {
	case req.VPN && req.VPNKind != "":
		b.WriteString(" for the " + req.VPNKind + " VPN")
	case req.SSID != "":
		b.WriteString(" for " + req.SSID)
	}
	if req.RequestNew {
		b.WriteString(" (the previous one was rejected)")
	}
	b.WriteString(".")
	if req.Message != "" {
		b.WriteString(" " + req.Message)
	}
	b.WriteString(" run: bnm secrets")
	return b.String()
}

// --- notify ------------------------------------------------------------------------------

// NotifyTest sends a test notification straight to the Notifier (not stored).
func (d *Daemon) NotifyTest(ctx context.Context) error {
	if d.o.Notifier == nil {
		return core.Errorf(core.KindUnsupported, "", "daemon: notifier is not wired in this build")
	}
	e := core.Event{
		Time: d.now(), Type: core.EventConnected, NetworkKey: d.Snapshot().Status.NetworkKey,
		Title: "bnm notifications work", Body: "This is a test notification from bnmd.", Urgency: "low",
		Data: map[string]string{"test": "true"},
	}
	// A Notifier that can bypass its own filter (debounce, rate limit, mutes)
	// should, so "bnm notify test" always shows something.
	if dl, ok := d.o.Notifier.(interface {
		Deliver(context.Context, core.Event) error
	}); ok {
		return dl.Deliver(ctx, e)
	}
	return d.o.Notifier.Notify(ctx, e)
}

// Close releases the store.
func (d *Daemon) Close() error {
	return d.o.Store.Close()
}
