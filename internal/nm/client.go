// Package nm implements core.NetworkManager over NetworkManager's D-Bus API
// using github.com/godbus/dbus/v5 directly (see docs/research/networkmanager-dbus.md
// for every interface, method and signature it relies on).
//
// The client takes one snapshot of NM's object graph with
// org.freedesktop.DBus.ObjectManager.GetManagedObjects on /org/freedesktop and
// then keeps an in-memory cache current from signals (PropertiesChanged,
// InterfacesAdded/Removed, DeviceAdded/Removed, Device.StateChanged,
// AccessPointAdded/Removed, Settings.NewConnection/ConnectionRemoved,
// Connection.Updated/Removed, Connection.Active.StateChanged,
// VPN.Connection.VpnStateChanged, CheckPermissions). Read calls (Devices,
// WifiNetworks, Profiles, ...) are answered from the cache without a round
// trip; write calls go to NM and, where they start an activation, wait for the
// ActiveConnection to settle. Watch hands out coarse core.Change hints.
//
// Testing has three tiers:
//
//   - go test ./internal/nm/...: pure logic (state and security mapping, IP dict
//     encoding with exact D-Bus signatures, heuristics, error mapping).
//   - go test -tags integration: python-dbusmock's networkmanager template on a
//     private bus (see integration_test.go for the nix command). dbusmock gaps
//     bnm hit and works around in tests: the mock has no AgentManager/secret
//     agent, no WireGuard, VPN, IP4Config/IP6Config or PrimaryConnection
//     objects, no Update2/AddConnection2/AddAndActivateConnection2 (the client
//     falls back to the un-suffixed methods when NM reports UnknownMethod),
//     no Filename/VersionId on connections, no Device.Disconnect, activation
//     is instantaneous (no ACTIVATING step, no failure injection, so the
//     wrong-password path is unit-tested with synthetic signals), RequestScan
//     is a no-op, AddWiFiConnection only accepts a bare KEY_MGMT_PSK security
//     value and Settings.NewConnection is emitted with the access-point path
//     instead of the connection path (the cache resyncs the connection list
//     when a NewConnection path has no GetSettings).
//   - go test -tags live -run Live: read-only against this machine's real
//     NetworkManager.
//
// Out of scope for v1 (TODO): registering a SecretAgent (so NM could prompt
// for secrets the profile does not store) and WPA-EAP networks.
package nm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"sync"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

// Option configures New.
type Option func(*Client)

// WithConn uses an existing bus connection (tests point it at a private bus).
// The connection must have been created with
// dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()). The client does
// not close it.
func WithConn(conn *dbus.Conn) Option {
	return func(c *Client) { c.conn = conn; c.ownConn = false }
}

// WithLogger sets the logger (default slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.log = l
		}
	}
}

// activeEvent is one Connection.Active.StateChanged delivered to a waiter.
type activeEvent struct {
	state, reason uint32
}

// Client is the D-Bus NetworkManager adapter. Safe for concurrent use.
type Client struct {
	conn    *dbus.Conn
	ownConn bool
	log     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	sigCh  chan *dbus.Signal
	done   chan struct{}

	mu       sync.RWMutex
	objs     map[dbus.ObjectPath]map[string]props // path -> iface -> props
	settings map[dbus.ObjectPath]settingsDict     // Settings/N -> GetSettings
	devRsn   map[dbus.ObjectPath]uint32           // last Device.StateChanged reason

	subMu   sync.Mutex
	subs    map[chan core.Change]struct{}
	waiters map[dbus.ObjectPath][]chan activeEvent

	// test seams
	sysfs    sysfsProbe
	username string
	nmcli    string
	uptime   func() (float64, bool)
	// activateTimeout bounds how long ConnectWifi/Activate wait for NM to settle.
	activateTimeout time.Duration
}

var _ core.NetworkManager = (*Client)(nil)

// New connects to the system bus (unless WithConn is given), takes the startup
// snapshot and starts the signal loop. ctx bounds the constructor and the
// lifetime of the signal loop: when ctx ends the client stops as if Close was
// called.
func New(ctx context.Context, opts ...Option) (*Client, error) {
	c := &Client{
		ownConn:         true,
		log:             slog.Default(),
		objs:            map[dbus.ObjectPath]map[string]props{},
		settings:        map[dbus.ObjectPath]settingsDict{},
		devRsn:          map[dbus.ObjectPath]uint32{},
		subs:            map[chan core.Change]struct{}{},
		waiters:         map[dbus.ObjectPath][]chan activeEvent{},
		sysfs:           sysfsClassNet("/sys/class/net"),
		username:        currentUser(),
		nmcli:           "nmcli",
		uptime:          readUptime,
		activateTimeout: 45 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	if c.conn == nil {
		conn, err := dbus.ConnectSystemBus(dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()))
		if err != nil {
			return nil, newErr("connect system bus", ErrUnavailable, err.Error())
		}
		c.conn = conn
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})

	// Subscribe before the snapshot so nothing is missed in between.
	c.sigCh = make(chan *dbus.Signal, 256)
	c.conn.Signal(c.sigCh)
	rules := [][]dbus.MatchOption{
		{dbus.WithMatchSender(busName), dbus.WithMatchPathNamespace(pathNM)},
		{dbus.WithMatchSender(busName), dbus.WithMatchObjectPath(pathRoot), dbus.WithMatchInterface(ifaceObjectManager)},
		{dbus.WithMatchSender(ifaceDBus), dbus.WithMatchInterface(ifaceDBus), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, busName)},
	}
	for _, r := range rules {
		if err := c.conn.AddMatchSignalContext(ctx, r...); err != nil {
			c.teardown()
			return nil, wrapDBus("subscribe to signals", err)
		}
	}
	if err := c.resync(ctx); err != nil {
		c.teardown()
		return nil, err
	}
	go c.loop()
	return c, nil
}

func (c *Client) teardown() {
	c.cancel()
	c.conn.RemoveSignal(c.sigCh)
	if c.ownConn {
		_ = c.conn.Close()
	}
}

// Close stops the signal loop, closes Watch channels and (when the client
// opened the bus connection itself) the connection.
func (c *Client) Close() error {
	c.cancel()
	<-c.done
	c.conn.RemoveSignal(c.sigCh)
	c.closeSubs()
	if c.ownConn {
		if err := c.conn.Close(); err != nil && !errors.Is(err, dbus.ErrClosed) {
			return fmt.Errorf("nm: close: %w", err)
		}
	}
	return nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// readUptime returns CLOCK_BOOTTIME-ish seconds (what AccessPoint.LastSpeen and
// Device.Wireless.LastScan count in).
func readUptime() (float64, bool) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	var up, idle float64
	if _, err := fmt.Sscanf(string(b), "%f %f", &up, &idle); err != nil {
		return 0, false
	}
	return up, true
}

// ---- bus helpers ----

func (c *Client) obj(path dbus.ObjectPath) dbus.BusObject {
	return c.conn.Object(busName, path)
}

// call invokes method on path and stores the reply into out.
func (c *Client) call(ctx context.Context, path dbus.ObjectPath, method string, args []any, out ...any) error {
	return c.obj(path).CallWithContext(ctx, method, 0, args...).Store(out...)
}

func (c *Client) getAll(ctx context.Context, path dbus.ObjectPath, iface string) (props, error) {
	var p props
	err := c.obj(path).CallWithContext(ctx, ifaceProperties+".GetAll", 0, iface).Store(&p)
	return p, err
}

func (c *Client) getProp(ctx context.Context, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := c.obj(path).CallWithContext(ctx, ifaceProperties+".Get", 0, iface, name).Store(&v)
	return v, err
}

func (c *Client) getSettings(ctx context.Context, path dbus.ObjectPath) (settingsDict, error) {
	var s settingsDict
	err := c.obj(path).CallWithContext(ctx, ifaceConnection+".GetSettings", 0).Store(&s)
	return s, err
}

// ---- cache ----

// resync replaces the whole cache from GetManagedObjects and GetSettings.
func (c *Client) resync(ctx context.Context) error {
	var managed map[dbus.ObjectPath]map[string]props
	if err := c.call(ctx, pathRoot, ifaceObjectManager+".GetManagedObjects", nil, &managed); err != nil {
		return wrapDBus("snapshot NetworkManager objects", err)
	}
	if _, ok := managed[pathNM]; !ok {
		return newErr("snapshot NetworkManager objects", ErrUnavailable,
			"the ObjectManager on /org/freedesktop lists no /org/freedesktop/NetworkManager")
	}
	settings := map[dbus.ObjectPath]settingsDict{}
	for path, ifaces := range managed {
		if _, ok := ifaces[ifaceConnection]; !ok {
			continue
		}
		s, err := c.getSettings(ctx, path)
		if err != nil {
			// Invisible to us per connection.permissions, or gone; skip.
			c.log.Debug("nm: GetSettings skipped", "path", path, "err", err)
			continue
		}
		settings[path] = s
	}
	c.mu.Lock()
	c.objs = managed
	c.settings = settings
	c.mu.Unlock()
	return nil
}

// fetchObject loads (or refreshes) every interface we care about on path.
func (c *Client) fetchObject(ctx context.Context, path dbus.ObjectPath) {
	ifaces := candidateIfaces(path)
	got := map[string]props{}
	for _, iface := range ifaces {
		p, err := c.getAll(ctx, path, iface)
		if err != nil {
			continue
		}
		got[iface] = p
	}
	var s settingsDict
	if _, ok := got[ifaceConnection]; ok {
		if sd, err := c.getSettings(ctx, path); err == nil {
			s = sd
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(got) == 0 {
		return
	}
	cur := c.objs[path]
	if cur == nil {
		cur = map[string]props{}
		c.objs[path] = cur
	}
	for iface, p := range got {
		cur[iface] = p
	}
	if s != nil {
		c.settings[path] = s
	}
}

// candidateIfaces guesses the interfaces an NM path can carry from its prefix.
func candidateIfaces(path dbus.ObjectPath) []string {
	p := string(path)
	switch {
	case hasPrefix(p, "/org/freedesktop/NetworkManager/Devices/"):
		return []string{ifaceDevice, ifaceWireless, ifaceWired, ifaceWireGuard}
	case hasPrefix(p, "/org/freedesktop/NetworkManager/AccessPoint/"):
		return []string{ifaceAP}
	case hasPrefix(p, "/org/freedesktop/NetworkManager/ActiveConnection/"):
		return []string{ifaceActive, ifaceVPN}
	case hasPrefix(p, "/org/freedesktop/NetworkManager/Settings/"):
		return []string{ifaceConnection}
	case hasPrefix(p, "/org/freedesktop/NetworkManager/IP4Config/"):
		return []string{ifaceIP4Config}
	case hasPrefix(p, "/org/freedesktop/NetworkManager/IP6Config/"):
		return []string{ifaceIP6Config}
	case path == pathNM:
		return []string{ifaceNM}
	case path == pathSettings:
		return []string{ifaceSettings}
	}
	return nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) > len(prefix) && s[:len(prefix)] == prefix
}

func (c *Client) removeObject(path dbus.ObjectPath) {
	c.mu.Lock()
	delete(c.objs, path)
	delete(c.settings, path)
	delete(c.devRsn, path)
	c.mu.Unlock()
}

// snapshot copies the cache references under the read lock. The maps are never
// mutated in place after publication except through set*, which replace the
// inner props map; callers must treat what they get as read-only.
func (c *Client) snapshot() (map[dbus.ObjectPath]map[string]props, map[dbus.ObjectPath]settingsDict) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	objs := make(map[dbus.ObjectPath]map[string]props, len(c.objs))
	for p, ifaces := range c.objs {
		cp := make(map[string]props, len(ifaces))
		for i, pr := range ifaces {
			cp[i] = pr
		}
		objs[p] = cp
	}
	settings := make(map[dbus.ObjectPath]settingsDict, len(c.settings))
	for p, s := range c.settings {
		settings[p] = s
	}
	return objs, settings
}

// setProps merges changed into path/iface (copy-on-write so readers holding an
// old props map are safe) and drops invalidated keys.
func (c *Client) setProps(path dbus.ObjectPath, iface string, changed props, invalidated []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ifaces := c.objs[path]
	if ifaces == nil {
		ifaces = map[string]props{}
		c.objs[path] = ifaces
	}
	old := ifaces[iface]
	next := make(props, len(old)+len(changed))
	for k, v := range old {
		next[k] = v
	}
	for k, v := range changed {
		next[k] = v
	}
	for _, k := range invalidated {
		delete(next, k)
	}
	ifaces[iface] = next
}

func (c *Client) propsOf(path dbus.ObjectPath, iface string) props {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.objs[path][iface]
}

// ---- signal loop ----

func (c *Client) loop() {
	defer close(c.done)
	for {
		select {
		case <-c.ctx.Done():
			return
		case sig, ok := <-c.sigCh:
			if !ok {
				return
			}
			c.handle(sig)
		}
	}
}

func (c *Client) handle(sig *dbus.Signal) {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	switch sig.Name {
	case ifaceProperties + ".PropertiesChanged":
		if len(sig.Body) < 2 {
			return
		}
		iface, _ := sig.Body[0].(string)
		changed, _ := sig.Body[1].(map[string]dbus.Variant)
		var invalidated []string
		if len(sig.Body) > 2 {
			invalidated, _ = sig.Body[2].([]string)
		}
		c.setProps(sig.Path, iface, changed, invalidated)
		c.hintFor(iface, sig.Path)

	case ifaceObjectManager + ".InterfacesAdded":
		if len(sig.Body) < 2 {
			return
		}
		path, _ := sig.Body[0].(dbus.ObjectPath)
		added, _ := sig.Body[1].(map[string]map[string]dbus.Variant)
		for iface, p := range added {
			c.setProps(path, iface, p, nil)
			c.hintFor(iface, path)
		}
		if _, ok := added[ifaceConnection]; ok {
			if s, err := c.getSettings(ctx, path); err == nil {
				c.mu.Lock()
				c.settings[path] = s
				c.mu.Unlock()
			}
		}

	case ifaceObjectManager + ".InterfacesRemoved":
		if len(sig.Body) < 1 {
			return
		}
		path, _ := sig.Body[0].(dbus.ObjectPath)
		c.mu.RLock()
		ifaces := c.objs[path]
		c.mu.RUnlock()
		for iface := range ifaces {
			c.hintFor(iface, path)
		}
		c.removeObject(path)

	case ifaceNM + ".DeviceAdded":
		if p, ok := pathArg(sig, 0); ok {
			if c.propsOf(p, ifaceDevice) == nil {
				c.fetchObject(ctx, p)
			}
			c.emit(core.ChangeDevices, p)
		}
	case ifaceNM + ".DeviceRemoved":
		if p, ok := pathArg(sig, 0); ok {
			c.removeObject(p)
			c.emit(core.ChangeDevices, p)
		}
	case ifaceNM + ".StateChanged":
		if len(sig.Body) > 0 {
			if s, ok := sig.Body[0].(uint32); ok {
				c.setProps(pathNM, ifaceNM, props{"State": dbus.MakeVariant(s)}, nil)
			}
		}
		c.emit(core.ChangeStatus, sig.Path)
	case ifaceNM + ".CheckPermissions":
		c.emit(core.ChangeStatus, sig.Path)

	case ifaceDevice + ".StateChanged":
		if len(sig.Body) >= 3 {
			ns, _ := sig.Body[0].(uint32)
			reason, _ := sig.Body[2].(uint32)
			c.setProps(sig.Path, ifaceDevice, props{"State": dbus.MakeVariant(ns)}, nil)
			c.mu.Lock()
			c.devRsn[sig.Path] = reason
			c.mu.Unlock()
		}
		c.emit(core.ChangeDevices, sig.Path)

	case ifaceWireless + ".AccessPointAdded":
		if p, ok := pathArg(sig, 0); ok {
			if c.propsOf(p, ifaceAP) == nil {
				c.fetchObject(ctx, p)
			}
		}
		c.emit(core.ChangeWifi, sig.Path)
	case ifaceWireless + ".AccessPointRemoved":
		if p, ok := pathArg(sig, 0); ok {
			c.removeObject(p)
		}
		c.emit(core.ChangeWifi, sig.Path)

	case ifaceSettings + ".NewConnection":
		p, ok := pathArg(sig, 0)
		if ok {
			if s, err := c.getSettings(ctx, p); err == nil {
				c.fetchObject(ctx, p)
				c.mu.Lock()
				c.settings[p] = s
				c.mu.Unlock()
			} else {
				// dbusmock emits the AP path here; fall back to the list.
				c.resyncConnections(ctx)
			}
		}
		c.emit(core.ChangeProfiles, p)
	case ifaceSettings + ".ConnectionRemoved":
		if p, ok := pathArg(sig, 0); ok {
			c.removeObject(p)
			c.emit(core.ChangeProfiles, p)
		}
	case ifaceConnection + ".Updated":
		c.fetchObject(ctx, sig.Path)
		c.emit(core.ChangeProfiles, sig.Path)
	case ifaceConnection + ".Removed":
		c.removeObject(sig.Path)
		c.emit(core.ChangeProfiles, sig.Path)

	case ifaceActive + ".StateChanged":
		if len(sig.Body) >= 2 {
			st, _ := sig.Body[0].(uint32)
			reason, _ := sig.Body[1].(uint32)
			c.setProps(sig.Path, ifaceActive, props{"State": dbus.MakeVariant(st)}, nil)
			c.notifyWaiters(sig.Path, activeEvent{st, reason})
		}
		c.emit(core.ChangeActive, sig.Path)
	case ifaceVPN + ".VpnStateChanged":
		if len(sig.Body) >= 1 {
			st, _ := sig.Body[0].(uint32)
			c.setProps(sig.Path, ifaceVPN, props{"VpnState": dbus.MakeVariant(st)}, nil)
		}
		c.emit(core.ChangeVPN, sig.Path)

	case ifaceDBus + ".NameOwnerChanged":
		if len(sig.Body) >= 3 {
			newOwner, _ := sig.Body[2].(string)
			if newOwner == "" {
				c.log.Warn("nm: NetworkManager left the bus")
				c.mu.Lock()
				c.objs = map[dbus.ObjectPath]map[string]props{}
				c.settings = map[dbus.ObjectPath]settingsDict{}
				c.mu.Unlock()
			} else {
				c.log.Info("nm: NetworkManager (re)appeared on the bus, resyncing")
				if err := c.resync(ctx); err != nil {
					c.log.Warn("nm: resync failed", "err", err)
				}
			}
			for _, k := range []core.ChangeKind{core.ChangeStatus, core.ChangeDevices, core.ChangeWifi, core.ChangeProfiles, core.ChangeActive} {
				c.emit(k, pathNM)
			}
		}
	}
}

// resyncConnections reloads the Settings.Connections list and any connection
// missing from the cache.
func (c *Client) resyncConnections(ctx context.Context) {
	v, err := c.getProp(ctx, pathSettings, ifaceSettings, "Connections")
	if err != nil {
		return
	}
	paths, _ := v.Value().([]dbus.ObjectPath)
	c.setProps(pathSettings, ifaceSettings, props{"Connections": v}, nil)
	for _, p := range paths {
		c.mu.RLock()
		_, have := c.settings[p]
		c.mu.RUnlock()
		if !have {
			c.fetchObject(ctx, p)
		}
	}
}

func pathArg(sig *dbus.Signal, i int) (dbus.ObjectPath, bool) {
	if len(sig.Body) <= i {
		return "", false
	}
	p, ok := sig.Body[i].(dbus.ObjectPath)
	return p, ok
}

// hintFor maps a changed interface onto a Change kind.
func (c *Client) hintFor(iface string, path dbus.ObjectPath) {
	switch iface {
	case ifaceNM:
		c.emit(core.ChangeStatus, path)
	case ifaceDevice, ifaceWired, ifaceWireGuard, ifaceIP4Config, ifaceIP6Config:
		c.emit(core.ChangeDevices, path)
	case ifaceWireless, ifaceAP:
		c.emit(core.ChangeWifi, path)
	case ifaceSettings, ifaceConnection:
		c.emit(core.ChangeProfiles, path)
	case ifaceActive:
		c.emit(core.ChangeActive, path)
	case ifaceVPN:
		c.emit(core.ChangeVPN, path)
	}
}

// ---- Watch ----

const watchBuffer = 64

// Watch delivers coarse Change hints until ctx ends or the client closes. The
// first hint is a ChangeStatus so a consumer can do its initial read on the same
// code path. The channel is buffered and hints are dropped when it is full.
func (c *Client) Watch(ctx context.Context) (<-chan core.Change, error) {
	ch := make(chan core.Change, watchBuffer)
	c.subMu.Lock()
	if c.subs == nil {
		c.subMu.Unlock()
		close(ch)
		return ch, newErr("watch", ErrUnavailable, "client is closed")
	}
	c.subs[ch] = struct{}{}
	c.subMu.Unlock()
	ch <- core.Change{Kind: core.ChangeStatus, Path: string(pathNM)}
	go func() {
		select {
		case <-ctx.Done():
		case <-c.ctx.Done():
		}
		c.subMu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.subMu.Unlock()
	}()
	return ch, nil
}

func (c *Client) emit(kind core.ChangeKind, path dbus.ObjectPath) {
	ch := core.Change{Kind: kind, Path: string(path)}
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for s := range c.subs {
		select {
		case s <- ch:
		default:
		}
	}
}

func (c *Client) closeSubs() {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for s := range c.subs {
		close(s)
	}
	c.subs = nil
	for p, ws := range c.waiters {
		for _, w := range ws {
			close(w)
		}
		delete(c.waiters, p)
	}
}

// ---- activation waiters ----

func (c *Client) addWaiter(path dbus.ObjectPath) chan activeEvent {
	ch := make(chan activeEvent, 16)
	c.subMu.Lock()
	c.waiters[path] = append(c.waiters[path], ch)
	c.subMu.Unlock()
	return ch
}

func (c *Client) removeWaiter(path dbus.ObjectPath, ch chan activeEvent) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	ws := c.waiters[path]
	for i, w := range ws {
		if w == ch {
			c.waiters[path] = append(ws[:i:i], ws[i+1:]...)
			break
		}
	}
	if len(c.waiters[path]) == 0 {
		delete(c.waiters, path)
	}
}

func (c *Client) notifyWaiters(path dbus.ObjectPath, ev activeEvent) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for _, w := range c.waiters[path] {
		select {
		case w <- ev:
		default:
		}
	}
}
