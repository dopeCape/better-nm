package nm

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

// accessPoint is one decoded AccessPoint object.
type accessPoint struct {
	path      dbus.ObjectPath
	ssid      string
	bssid     string
	strength  uint8
	frequency uint32
	security  core.WifiSecurity
	lastSeen  int64 // CLOCK_BOOTTIME seconds, -1 unknown
}

func decodeAP(path dbus.ObjectPath, p props) accessPoint {
	ap := accessPoint{
		path:      path,
		ssid:      ssidString(vBytes(p, "Ssid")),
		bssid:     vStr(p, "HwAddress"),
		strength:  vU8(p, "Strength"),
		frequency: vU32(p, "Frequency"),
		security:  apSecurity(vU32(p, "Flags"), vU32(p, "WpaFlags"), vU32(p, "RsnFlags")),
		lastSeen:  -1,
	}
	if ls, ok := vI64(p, "LastSeen"); ok {
		ap.lastSeen = ls
	}
	return ap
}

// wifiDevices lists Wi-Fi device paths, filtered to name when non-empty.
func (v view) wifiDevices(name string) []dbus.ObjectPath {
	var out []dbus.ObjectPath
	for _, p := range v.pathsWith(ifaceWireless) {
		if name != "" && v.deviceName(p) != name {
			continue
		}
		out = append(out, p)
	}
	return out
}

// wifiProfiles indexes Wi-Fi profiles by SSID -> [(uuid, interface-name)].
type wifiProfile struct {
	uuid, iface string
	path        dbus.ObjectPath
}

func (v view) wifiProfiles() map[string][]wifiProfile {
	out := map[string][]wifiProfile{}
	for p, s := range v.settings {
		if vStr(s[settingConnection], "type") != typeWifi {
			continue
		}
		ssid := ssidString(vBytes(s[settingWifi], "ssid"))
		if ssid == "" {
			continue
		}
		out[ssid] = append(out[ssid], wifiProfile{
			uuid:  vStr(s[settingConnection], "uuid"),
			iface: vStr(s[settingConnection], "interface-name"),
			path:  p,
		})
	}
	return out
}

// pickProfile prefers a profile bound to dev (or unbound) over one bound elsewhere.
func pickProfile(cands []wifiProfile, dev string) (wifiProfile, bool) {
	var fallback *wifiProfile
	for i := range cands {
		c := cands[i]
		if c.iface == "" || c.iface == dev {
			return c, true
		}
		if fallback == nil {
			fallback = &cands[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return wifiProfile{}, false
}

// WifiNetworks aggregates access points per SSID per Wi-Fi device.
func (c *Client) WifiNetworks(ctx context.Context, device string) ([]core.WifiNetwork, error) {
	v := c.view()
	if !v.has(pathNM, ifaceNM) {
		return nil, newErr("list wifi networks", ErrUnavailable, "no NetworkManager object in cache")
	}
	devs := v.wifiDevices(device)
	if device != "" && len(devs) == 0 {
		return nil, newErr("list wifi networks", ErrNotFound, "no Wi-Fi device named "+device)
	}
	profiles := v.wifiProfiles()
	now := time.Now()
	uptime, haveUptime := c.uptime()
	var out []core.WifiNetwork
	for _, dp := range devs {
		devName := v.deviceName(dp)
		w := v.props(dp, ifaceWireless)
		activeAP := vPath(w, "ActiveAccessPoint")
		if !realPath(activeAP) {
			// Older NM / mocks: the active connection's SpecificObject is the AP.
			if ac := vPath(v.props(dp, ifaceDevice), "ActiveConnection"); realPath(ac) {
				activeAP = vPath(v.props(ac, ifaceActive), "SpecificObject")
			}
		}
		bySSID := map[string]*core.WifiNetwork{}
		var order []string
		for _, app := range vPaths(w, "AccessPoints") {
			p := v.props(app, ifaceAP)
			if p == nil {
				continue
			}
			ap := decodeAP(app, p)
			if ap.ssid == "" {
				continue // hidden: nothing to aggregate on
			}
			n := bySSID[ap.ssid]
			if n == nil {
				n = &core.WifiNetwork{SSID: ap.ssid, Device: devName, Security: ap.security}
				bySSID[ap.ssid] = n
				order = append(order, ap.ssid)
				if prof, ok := pickProfile(profiles[ap.ssid], devName); ok {
					n.Known, n.ProfileUUID = true, prof.uuid
				}
			}
			if ap.bssid != "" {
				n.BSSIDs = append(n.BSSIDs, ap.bssid)
			}
			if ap.strength >= n.Strength && (ap.strength > n.Strength || n.Frequency == 0) {
				n.Strength = ap.strength
				n.Frequency = ap.frequency
				n.Channel, n.Band = wifiChannel(ap.frequency)
				n.Security = ap.security
			}
			if app == activeAP {
				n.Active = true
			}
			if haveUptime && ap.lastSeen >= 0 {
				seen := now.Add(-time.Duration((uptime - float64(ap.lastSeen)) * float64(time.Second)))
				if seen.After(n.LastSeen) {
					n.LastSeen = seen
				}
			}
		}
		for _, ssid := range order {
			out = append(out, *bySSID[ssid])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].Strength > out[j].Strength
	})
	return out, nil
}

// Scan asks device (or the first Wi-Fi device) to rescan and returns once NM
// accepted the request. Results arrive later as AccessPointAdded / LastScan
// changes, surfaced through Watch as ChangeWifi.
func (c *Client) Scan(ctx context.Context, device string) error {
	v := c.view()
	dp, err := v.pickWifiDevice(device)
	if err != nil {
		return err
	}
	if err := c.call(ctx, dp, ifaceWireless+".RequestScan", []any{map[string]dbus.Variant{}}); err != nil {
		return wrapDBus("scan "+v.deviceName(dp), err)
	}
	return nil
}

// pickWifiDevice resolves "" to the first usable Wi-Fi device.
func (v view) pickWifiDevice(name string) (dbus.ObjectPath, error) {
	devs := v.wifiDevices(name)
	if len(devs) == 0 {
		if name != "" {
			return "", newErr("find wifi device "+name, ErrNotFound, "no Wi-Fi device with that name")
		}
		return "", newErr("find wifi device", ErrNotFound, "this machine has no Wi-Fi device NetworkManager manages")
	}
	if name != "" {
		return devs[0], nil
	}
	// Prefer a device that can actually connect.
	for _, d := range devs {
		if vU32(v.props(d, ifaceDevice), "State") >= devStateDisconnected {
			return d, nil
		}
	}
	return devs[0], nil
}

// bestAP returns the strongest visible AP for ssid on dev, if any.
func (v view) bestAP(dev dbus.ObjectPath, ssid string) (accessPoint, bool) {
	var best accessPoint
	found := false
	for _, app := range vPaths(v.props(dev, ifaceWireless), "AccessPoints") {
		p := v.props(app, ifaceAP)
		if p == nil {
			continue
		}
		ap := decodeAP(app, p)
		if ap.ssid != ssid {
			continue
		}
		if !found || ap.strength > best.strength {
			best, found = ap, true
		}
	}
	return best, found
}

// ConnectWifi joins req.SSID on req.Device. With a saved profile for the SSID it
// activates that (refreshing the PSK when a password is given); otherwise it
// creates one with AddAndActivateConnection2, scoped to the current user,
// with the secret stored system-owned (psk-flags 0) so no agent is needed. It
// returns once the ActiveConnection is activated, or with a translated error
// when it deactivated / timed out.
func (c *Client) ConnectWifi(ctx context.Context, req core.ConnectWifiRequest) error {
	op := "connect wifi " + req.SSID
	if strings.TrimSpace(req.SSID) == "" {
		return newErr(op, nil, "SSID is required")
	}
	v := c.view()
	dev, err := v.pickWifiDevice(req.Device)
	if err != nil {
		return err
	}
	devName := v.deviceName(dev)
	ap, haveAP := v.bestAP(dev, req.SSID)
	apPath := dbus.ObjectPath("/")
	if haveAP {
		apPath = ap.path
	}

	// Saved profile: activate it.
	if prof, ok := pickProfile(v.wifiProfiles()[req.SSID], devName); ok {
		if req.Password != "" {
			if err := c.updatePSK(ctx, prof.path, req.Password); err != nil {
				return err
			}
		}
		var active dbus.ObjectPath
		if err := c.call(ctx, pathNM, ifaceNM+".ActivateConnection",
			[]any{prof.path, dev, apPath}, &active); err != nil {
			return wrapDBus(op, err)
		}
		return c.waitActive(ctx, op, active)
	}

	// New profile.
	sec := core.SecOpen
	if haveAP {
		sec = ap.security
	} else if req.Password != "" {
		sec = core.SecWPAPSK // hidden or not yet seen: assume WPA-PSK
	}
	if req.Username != "" || sec == core.SecWPAEAP {
		// TODO(v2): WPA-EAP needs 802-1x settings and usually a secret agent.
		return newErr(op, ErrUnsupported, "WPA-EAP (enterprise) networks are unsupported in v1")
	}
	hidden := req.Hidden || !haveAP
	settings, err := wifiPartialSettings(req.SSID, hidden, sec, req.Password, c.username)
	if err != nil {
		return err
	}
	var (
		connPath, active dbus.ObjectPath
		result           map[string]dbus.Variant
	)
	err = c.call(ctx, pathNM, ifaceNM+".AddAndActivateConnection2",
		[]any{settings, dev, apPath, map[string]dbus.Variant{}}, &connPath, &active, &result)
	if isUnknownMethod(err) {
		err = c.call(ctx, pathNM, ifaceNM+".AddAndActivateConnection",
			[]any{settings, dev, apPath}, &connPath, &active)
	}
	if err != nil {
		return wrapDBus(op, err)
	}
	if err := c.waitActive(ctx, op, active); err != nil {
		// Don't leave a profile behind that never worked.
		if errors.Is(err, ErrAuthFailed) || errors.Is(err, ErrNoSecrets) {
			_ = c.call(context.WithoutCancel(ctx), connPath, ifaceConnection+".Delete", nil)
		}
		return err
	}
	return nil
}

// updatePSK stores a new system-owned PSK in an existing profile.
func (c *Client) updatePSK(ctx context.Context, path dbus.ObjectPath, psk string) error {
	return c.editSettings(ctx, "update wifi password", path, func(s settingsDict) error {
		sec := s[settingWifiSecurity]
		if sec == nil {
			sec = props{"key-mgmt": dbus.MakeVariant("wpa-psk")}
			s[settingWifiSecurity] = sec
			s[settingWifi]["security"] = dbus.MakeVariant(settingWifiSecurity)
		}
		sec["psk"] = dbus.MakeVariant(psk)
		sec["psk-flags"] = dbus.MakeVariant(secretFlagsNone)
		return nil
	})
}

// waitActive blocks until the ActiveConnection at path is activated (nil) or
// deactivated (error), bounded by c.activateTimeout.
func (c *Client) waitActive(ctx context.Context, op string, path dbus.ObjectPath) error {
	if !realPath(path) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.activateTimeout)
	defer cancel()
	ch := c.addWaiter(path)
	defer c.removeWaiter(path, ch)

	// Read the current state live: the mock (and a fast NM) may already be done.
	if v, err := c.getProp(ctx, path, ifaceActive, "State"); err == nil {
		if st, ok := v.Value().(uint32); ok {
			switch st {
			case activeActivated:
				return nil
			case activeDeactivated:
				return c.activationError(op, path, activeReasonUnknown)
			}
		}
	}
	return c.waitEvents(ctx, op, path, ch)
}

// waitLoop is waitActive without the live pre-read (signal-driven only).
func (c *Client) waitLoop(ctx context.Context, op string, path dbus.ObjectPath) error {
	ctx, cancel := context.WithTimeout(ctx, c.activateTimeout)
	defer cancel()
	ch := c.addWaiter(path)
	defer c.removeWaiter(path, ch)
	return c.waitEvents(ctx, op, path, ch)
}

func (c *Client) waitEvents(ctx context.Context, op string, path dbus.ObjectPath, ch chan activeEvent) error {
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return newErr(op, ErrTimeout, "NetworkManager did not finish activating within "+c.activateTimeout.String())
			}
			return newErr(op, nil, ctx.Err().Error())
		case ev, ok := <-ch:
			if !ok {
				return newErr(op, ErrUnavailable, "client closed")
			}
			switch ev.state {
			case activeActivated:
				return nil
			case activeDeactivated:
				return c.activationError(op, path, ev.reason)
			}
		}
	}
}

// activationError translates why an activation ended into a typed error, using
// the device's last StateChanged reason for the Wi-Fi specifics.
func (c *Client) activationError(op string, path dbus.ObjectPath, reason uint32) error {
	v := c.view()
	msg := activeReasonText(reason)
	kind := error(nil)
	if activeReasonIsAuth(reason) {
		kind = ErrAuthFailed
	}
	c.mu.RLock()
	var devReason uint32
	for _, dp := range vPaths(v.props(path, ifaceActive), "Devices") {
		if r, ok := c.devRsn[dp]; ok && r != devReasonNone {
			devReason = r
			break
		}
	}
	c.mu.RUnlock()
	if t := devReasonText(devReason); t != "" {
		msg = t
		if devReasonIsAuth(devReason) {
			kind = ErrAuthFailed
		} else if devReason == devReasonSSIDNotFound {
			kind = ErrNotFound
		}
	}
	if kind == nil && reason == activeReasonNoSecrets {
		kind = ErrNoSecrets
	}
	return newErr(op, kind, msg)
}

// isUnknownMethod says whether err is D-Bus "no such method" (older NM, dbusmock).
func isUnknownMethod(err error) bool {
	if err == nil {
		return false
	}
	e := wrapDBus("", err)
	if ne, ok := e.(*Error); ok {
		return ne.Name == "org.freedesktop.DBus.Error.UnknownMethod"
	}
	return false
}
