package nm

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

// view is one consistent read of the cache plus the derived indexes the
// read methods share.
type view struct {
	objs     map[dbus.ObjectPath]map[string]props
	settings map[dbus.ObjectPath]settingsDict
}

func (c *Client) view() view {
	o, s := c.snapshot()
	return view{objs: o, settings: s}
}

func (v view) props(path dbus.ObjectPath, iface string) props {
	return v.objs[path][iface]
}

func (v view) has(path dbus.ObjectPath, iface string) bool {
	_, ok := v.objs[path][iface]
	return ok
}

// pathsWith lists object paths carrying iface, in a stable order.
func (v view) pathsWith(iface string) []dbus.ObjectPath {
	var out []dbus.ObjectPath
	for p, ifaces := range v.objs {
		if _, ok := ifaces[iface]; ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return pathLess(out[i], out[j]) })
	return out
}

// pathLess orders .../Foo/2 before .../Foo/10.
func pathLess(a, b dbus.ObjectPath) bool {
	as, bs := string(a), string(b)
	ai, bi := strings.LastIndex(as, "/"), strings.LastIndex(bs, "/")
	if ai >= 0 && bi >= 0 && as[:ai] == bs[:bi] {
		an, aerr := strconv.Atoi(as[ai+1:])
		bn, berr := strconv.Atoi(bs[bi+1:])
		if aerr == nil && berr == nil {
			return an < bn
		}
	}
	return as < bs
}

// ipInfo is what an IP4Config/IP6Config pair contributes.
type ipInfo struct {
	v4, v6   []string
	gateway4 string
	dns      []string
}

func (v view) ipInfo(ip4, ip6 dbus.ObjectPath) ipInfo {
	var out ipInfo
	if realPath(ip4) {
		p := v.props(ip4, ifaceIP4Config)
		out.v4 = addressData(vDicts(p, "AddressData"))
		out.gateway4 = vStr(p, "Gateway")
		if nd := vDicts(p, "NameserverData"); len(nd) > 0 {
			for _, e := range nd {
				if a := vStr(e, "address"); a != "" {
					out.dns = append(out.dns, a)
				}
			}
		} else if ns, ok := p["Nameservers"].Value().([]uint32); ok {
			for _, u := range ns {
				out.dns = append(out.dns, ip4FromU32(u))
			}
		}
	}
	if realPath(ip6) {
		p := v.props(ip6, ifaceIP6Config)
		out.v6 = addressData(vDicts(p, "AddressData"))
		if ns, ok := p["Nameservers"].Value().([][]byte); ok {
			for _, b := range ns {
				if s := ip6FromBytes(b); s != "" {
					out.dns = append(out.dns, s)
				}
			}
		}
	}
	return out
}

// deviceName resolves a device path to its interface name.
func (v view) deviceName(p dbus.ObjectPath) string {
	return vStr(v.props(p, ifaceDevice), "Interface")
}

// devicePath finds a device by interface name.
func (v view) devicePath(name string) (dbus.ObjectPath, bool) {
	for _, p := range v.pathsWith(ifaceDevice) {
		if vStr(v.props(p, ifaceDevice), "Interface") == name {
			return p, true
		}
	}
	return "", false
}

// connByUUID finds a Settings/N path by profile UUID.
func (v view) connByUUID(uuid string) (dbus.ObjectPath, bool) {
	for p, s := range v.settings {
		if vStr(s[settingConnection], "uuid") == uuid {
			return p, true
		}
	}
	return "", false
}

// activeByUUID finds the ActiveConnection for a profile UUID.
func (v view) activeByUUID(uuid string) (dbus.ObjectPath, bool) {
	for _, p := range v.pathsWith(ifaceActive) {
		if vStr(v.props(p, ifaceActive), "Uuid") == uuid {
			return p, true
		}
	}
	return "", false
}

// activeUUIDs is the set of UUIDs with a live (activating/activated) connection.
func (v view) activeUUIDs() map[string]bool {
	out := map[string]bool{}
	for _, p := range v.pathsWith(ifaceActive) {
		a := v.props(p, ifaceActive)
		switch vU32(a, "State") {
		case activeActivating, activeActivated:
			out[vStr(a, "Uuid")] = true
		}
	}
	return out
}

// ---- Devices ----

// Devices lists every device NM knows, mapped to bnm's model.
func (c *Client) Devices(ctx context.Context) ([]core.Device, error) {
	v := c.view()
	if !v.has(pathNM, ifaceNM) {
		return nil, newErr("list devices", ErrUnavailable, "no NetworkManager object in cache")
	}
	var out []core.Device
	for _, p := range v.pathsWith(ifaceDevice) {
		if vU32(v.props(p, ifaceDevice), "DeviceType") == devTypeWifiP2P {
			continue // p2p-dev-* is not an interface the host has
		}
		out = append(out, c.device(v, p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c *Client) device(v view, p dbus.ObjectPath) core.Device {
	d := v.props(p, ifaceDevice)
	name := vStr(d, "Interface")
	kind := deviceKind(vU32(d, "DeviceType"))
	dev := core.Device{
		Name:    name,
		IfIndex: c.ifIndex(name),
		Kind:    kind,
		Class:   deviceClass(name, kind, c.sysfs),
		Managed: false,
		HwAddr:  vStr(d, "HwAddress"),
		Driver:  vStr(d, "Driver"),
		NMPath:  string(p),
	}
	dev.Managed, _ = vBool(d, "Managed")
	if dev.Class == core.ClassInfra {
		dev.Owner = deviceOwner(name, kind)
	}
	if w := v.props(p, ifaceWired); w != nil {
		if dev.HwAddr == "" {
			dev.HwAddr = vStr(w, "HwAddress")
		}
		dev.Speed = int(vU32(w, "Speed"))
	}
	if w := v.props(p, ifaceWireless); w != nil {
		if dev.HwAddr == "" {
			dev.HwAddr = vStr(w, "HwAddress")
		}
		dev.Speed = int(vU32(w, "Bitrate") / 1000)
	}
	external := false
	if ac := vPath(d, "ActiveConnection"); realPath(ac) {
		a := v.props(ac, ifaceActive)
		dev.ActiveUUID = vStr(a, "Uuid")
		dev.ActiveName = vStr(a, "Id")
		external = vU32(a, "StateFlags")&activeFlagExternal != 0
	}
	dev.State = deviceState(vU32(d, "State"), external)
	ip := v.ipInfo(vPath(d, "Ip4Config"), vPath(d, "Ip6Config"))
	dev.IPv4, dev.IPv6, dev.Gateway4, dev.DNS = ip.v4, ip.v6, ip.gateway4, ip.dns
	return dev
}

// ifIndex reads /sys/class/net/<name>/ifindex; 0 when unknown (NM does not
// export the ifindex on D-Bus).
func (c *Client) ifIndex(name string) int {
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return 0
	}
	b, err := os.ReadFile(filepath.Join("/sys/class/net", name, "ifindex"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// ---- ActiveConnections ----

// ActiveConnections lists NM's ActiveConnection objects.
func (c *Client) ActiveConnections(ctx context.Context) ([]core.ActiveConnection, error) {
	v := c.view()
	var out []core.ActiveConnection
	for _, p := range v.pathsWith(ifaceActive) {
		out = append(out, v.active(p))
	}
	return out, nil
}

func (v view) active(p dbus.ObjectPath) core.ActiveConnection {
	a := v.props(p, ifaceActive)
	ac := core.ActiveConnection{
		Path:        string(p),
		ProfileUUID: vStr(a, "Uuid"),
		ProfileName: vStr(a, "Id"),
		Type:        profileType(vStr(a, "Type")),
		State:       activeState(vU32(a, "State")),
		External:    vU32(a, "StateFlags")&activeFlagExternal != 0,
	}
	ac.Default4, _ = vBool(a, "Default")
	ac.Default6, _ = vBool(a, "Default6")
	ac.VPN, _ = vBool(a, "Vpn")
	for _, dp := range vPaths(a, "Devices") {
		if n := v.deviceName(dp); n != "" {
			ac.Devices = append(ac.Devices, n)
		}
	}
	if ac.Devices == nil {
		ac.Devices = []string{}
	}
	if vp := v.props(p, ifaceVPN); vp != nil {
		ac.VPN = true
		ac.VPNState = vpnStateName(vU32(vp, "VpnState"))
		ac.VPNBanner = vStr(vp, "Banner")
	}
	ip := v.ipInfo(vPath(a, "Ip4Config"), vPath(a, "Ip6Config"))
	ac.IPv4, ac.IPv6, ac.Gateway4, ac.DNS = ip.v4, ip.v6, ip.gateway4, ip.dns
	return ac
}

// ---- Status / Permissions ----

// Status is the one-screen summary. Permissions are fetched live (cheap, and
// CheckPermissions tells consumers when to re-read).
func (c *Client) Status(ctx context.Context) (core.Status, error) {
	v := c.view()
	nm := v.props(pathNM, ifaceNM)
	if nm == nil {
		return core.Status{}, newErr("status", ErrUnavailable, "no NetworkManager object in cache")
	}
	st := core.Status{
		NMState:      nmStateName(vU32(nm, "State")),
		Connectivity: connectivity(vU32(nm, "Connectivity")),
		NMVersion:    vStr(nm, "Version"),
	}
	st.Networking, _ = vBool(nm, "NetworkingEnabled")
	st.WifiEnabled, _ = vBool(nm, "WirelessEnabled")
	st.WifiHardware, _ = vBool(nm, "WirelessHardwareEnabled")
	if pc := vPath(nm, "PrimaryConnection"); realPath(pc) && v.has(pc, ifaceActive) {
		a := v.active(pc)
		st.Primary = &a
		st.NetworkKey = core.NetworkKey(st.Primary, v.profiles())
	}
	perms, err := c.Permissions(ctx)
	if err != nil {
		c.log.Debug("nm: GetPermissions failed", "err", err)
	} else {
		st.Permissions = perms
	}
	return st, nil
}

// Permissions returns GetPermissions: polkit action -> yes|auth|no. The
// answer is cached until NM says it may have changed (CheckPermissions, or NM
// leaving/joining the bus): Status runs on every NM signal batch, including
// the access-point strength updates that arrive every few seconds on Wi-Fi,
// and each GetPermissions costs NM a polkit check per action.
func (c *Client) Permissions(ctx context.Context) (map[string]string, error) {
	c.permMu.Lock()
	defer c.permMu.Unlock()
	if c.perms == nil {
		var perms map[string]string
		if err := c.call(ctx, pathNM, ifaceNM+".GetPermissions", nil, &perms); err != nil {
			return nil, wrapDBus("get permissions", err)
		}
		if perms == nil {
			perms = map[string]string{}
		}
		c.perms = perms
	}
	out := make(map[string]string, len(c.perms))
	for k, v := range c.perms {
		out[k] = v
	}
	return out, nil
}

// invalidatePermissions drops the cached GetPermissions answer.
func (c *Client) invalidatePermissions() {
	c.permMu.Lock()
	c.perms = nil
	c.permMu.Unlock()
}

// ---- Profiles ----

// Profiles lists every saved connection visible to this user.
func (c *Client) Profiles(ctx context.Context) ([]core.Profile, error) {
	v := c.view()
	if !v.has(pathNM, ifaceNM) {
		return nil, newErr("list profiles", ErrUnavailable, "no NetworkManager object in cache")
	}
	return v.profiles(), nil
}

func (v view) profiles() []core.Profile {
	active := v.activeUUIDs()
	paths := make([]dbus.ObjectPath, 0, len(v.settings))
	for p := range v.settings {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return pathLess(paths[i], paths[j]) })
	out := make([]core.Profile, 0, len(paths))
	for _, p := range paths {
		out = append(out, v.profile(p, active))
	}
	return out
}

func (v view) profile(p dbus.ObjectPath, active map[string]bool) core.Profile {
	s := v.settings[p]
	meta := connMeta{}
	if cp := v.props(p, ifaceConnection); cp != nil {
		meta.Filename = vStr(cp, "Filename")
		meta.VersionID = vU64(cp, "VersionId")
	}
	uuid := vStr(s[settingConnection], "uuid")
	return decodeProfile(p, s, meta, active[uuid])
}

// Profile returns one profile by UUID.
func (c *Client) Profile(ctx context.Context, uuid string) (core.Profile, error) {
	v := c.view()
	p, ok := v.connByUUID(uuid)
	if !ok {
		return core.Profile{}, newErr("profile "+uuid, ErrNotFound, "no profile with that UUID is visible to this user")
	}
	return v.profile(p, v.activeUUIDs()), nil
}
