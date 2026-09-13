package nm

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/godbus/dbus/v5"
)

// Activate activates a profile; device "" lets NM choose. It waits for the
// ActiveConnection to settle (see waitActive).
func (c *Client) Activate(ctx context.Context, profileUUID, device string) error {
	op := "activate " + profileUUID
	v := c.view()
	conn, ok := v.connByUUID(profileUUID)
	if !ok {
		return newErr(op, ErrNotFound, "no profile with that UUID is visible to this user")
	}
	dev := dbus.ObjectPath("/")
	if device != "" {
		dp, ok := v.devicePath(device)
		if !ok {
			return newErr(op, ErrNotFound, "no device named "+device)
		}
		dev = dp
	}
	var active dbus.ObjectPath
	if err := c.call(ctx, pathNM, ifaceNM+".ActivateConnection", []any{conn, dev, dbus.ObjectPath("/")}, &active); err != nil {
		return wrapDBus(op, err)
	}
	return c.waitActive(ctx, op, active)
}

// Deactivate deactivates the active connection of a profile.
func (c *Client) Deactivate(ctx context.Context, profileUUID string) error {
	op := "deactivate " + profileUUID
	v := c.view()
	active, ok := v.activeByUUID(profileUUID)
	if !ok {
		return newErr(op, ErrNotFound, "that profile is not active")
	}
	if err := c.call(ctx, pathNM, ifaceNM+".DeactivateConnection", []any{active}); err != nil {
		return wrapDBus(op, err)
	}
	return nil
}

// DisconnectDevice brings a device down and blocks autoconnect until the next
// manual activation (Device.Disconnect).
func (c *Client) DisconnectDevice(ctx context.Context, device string) error {
	op := "disconnect " + device
	v := c.view()
	dp, ok := v.devicePath(device)
	if !ok {
		return newErr(op, ErrNotFound, "no device named "+device)
	}
	if err := c.call(ctx, dp, ifaceDevice+".Disconnect", nil); err != nil {
		return wrapDBus(op, err)
	}
	return nil
}

// Forget deletes a profile.
func (c *Client) Forget(ctx context.Context, profileUUID string) error {
	op := "forget " + profileUUID
	v := c.view()
	conn, ok := v.connByUUID(profileUUID)
	if !ok {
		return newErr(op, ErrNotFound, "no profile with that UUID is visible to this user")
	}
	if err := c.call(ctx, conn, ifaceConnection+".Delete", nil); err != nil {
		return wrapDBus(op, err)
	}
	return nil
}

// SetWifiEnabled flips the WirelessEnabled property (polkit enable-disable-wifi).
func (c *Client) SetWifiEnabled(ctx context.Context, on bool) error {
	err := c.obj(pathNM).CallWithContext(ctx, ifaceProperties+".Set", 0, ifaceNM, "WirelessEnabled", dbus.MakeVariant(on)).Err
	if err != nil {
		return wrapDBus(fmt.Sprintf("set wifi enabled=%v", on), err)
	}
	return nil
}

// SetAutoconnect sets connection.autoconnect on a profile.
func (c *Client) SetAutoconnect(ctx context.Context, profileUUID string, on bool) error {
	op := fmt.Sprintf("set autoconnect=%v on %s", on, profileUUID)
	v := c.view()
	conn, ok := v.connByUUID(profileUUID)
	if !ok {
		return newErr(op, ErrNotFound, "no profile with that UUID is visible to this user")
	}
	return c.editSettings(ctx, op, conn, func(s settingsDict) error {
		s[settingConnection]["autoconnect"] = dbus.MakeVariant(on)
		return nil
	})
}

// UpdateIPConfig replaces the ipv4/ipv6 layer of a profile; nil leaves a family alone.
func (c *Client) UpdateIPConfig(ctx context.Context, profileUUID string, ipv4, ipv6 *core.IPConfig) error {
	op := "update ip config of " + profileUUID
	if ipv4 == nil && ipv6 == nil {
		return nil
	}
	v := c.view()
	conn, ok := v.connByUUID(profileUUID)
	if !ok {
		return newErr(op, ErrNotFound, "no profile with that UUID is visible to this user")
	}
	return c.editSettings(ctx, op, conn, func(s settingsDict) error {
		if ipv4 != nil {
			d, err := encodeIPConfig(s[settingIPv4], *ipv4, 4)
			if err != nil {
				return newErr(op, nil, err.Error())
			}
			s[settingIPv4] = d
		}
		if ipv6 != nil {
			d, err := encodeIPConfig(s[settingIPv6], *ipv6, 6)
			if err != nil {
				return newErr(op, nil, err.Error())
			}
			s[settingIPv6] = d
		}
		return nil
	})
}

// SetProfilePermissions scopes a profile to the current user (permissions =
// ["user:<name>:"], so later edits need only settings.modify.own) or makes it
// system-wide again.
func (c *Client) SetProfilePermissions(ctx context.Context, profileUUID string, userOnly bool) error {
	op := fmt.Sprintf("set permissions userOnly=%v on %s", userOnly, profileUUID)
	v := c.view()
	conn, ok := v.connByUUID(profileUUID)
	if !ok {
		return newErr(op, ErrNotFound, "no profile with that UUID is visible to this user")
	}
	if userOnly && c.username == "" {
		return newErr(op, nil, "cannot determine the current user name")
	}
	return c.editSettings(ctx, op, conn, func(s settingsDict) error {
		perms := []string{}
		if userOnly {
			perms = []string{"user:" + c.username + ":"}
		}
		s[settingConnection]["permissions"] = dbus.MakeVariant(perms)
		return nil
	})
}

// editSettings does GetSettings -> edit -> Update2(to-disk, version-id), falling
// back to Update on NM/mocks without Update2. The deprecated ipv4/ipv6
// addresses/routes arrays are stripped so address-data is honoured.
func (c *Client) editSettings(ctx context.Context, op string, conn dbus.ObjectPath, edit func(settingsDict) error) error {
	raw, err := c.getSettings(ctx, conn)
	if err != nil {
		return wrapDBus(op, err)
	}
	s := sanitizeForUpdate(raw)
	if s[settingConnection] == nil {
		s[settingConnection] = props{}
	}
	if err := edit(s); err != nil {
		return err
	}
	args := map[string]dbus.Variant{}
	if v, err := c.getProp(ctx, conn, ifaceConnection, "VersionId"); err == nil {
		if vid, ok := v.Value().(uint64); ok {
			args["version-id"] = dbus.MakeVariant(vid)
		}
	}
	var result map[string]dbus.Variant
	err = c.call(ctx, conn, ifaceConnection+".Update2", []any{s, updateToDisk, args}, &result)
	if isUnknownMethod(err) {
		err = c.call(ctx, conn, ifaceConnection+".Update", []any{s})
	}
	if err != nil {
		return wrapDBus(op, err)
	}
	// Keep the cache fresh even before Updated arrives.
	c.fetchObject(ctx, conn)
	return nil
}

// AddWireGuard creates an NM-native wireguard profile owned by the calling user
// and returns its UUID.
func (c *Client) AddWireGuard(ctx context.Context, spec core.WireGuardSpec) (string, error) {
	op := "add wireguard " + spec.Name
	uuid, err := newUUID()
	if err != nil {
		return "", fmt.Errorf("nm: %s: %w", op, err)
	}
	settings, err := wireguardSettings(spec, uuid, c.username)
	if err != nil {
		return "", err
	}
	var (
		path   dbus.ObjectPath
		result map[string]dbus.Variant
	)
	err = c.call(ctx, pathSettings, ifaceSettings+".AddConnection2",
		[]any{settings, updateToDisk, map[string]dbus.Variant{}}, &path, &result)
	if isUnknownMethod(err) {
		err = c.call(ctx, pathSettings, ifaceSettings+".AddConnection", []any{settings}, &path)
	}
	if err != nil {
		return "", wrapDBus(op, err)
	}
	c.fetchObject(ctx, path)
	return uuid, nil
}

var reUUID = regexp.MustCompile(`\(([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\)`)
var reKind = regexp.MustCompile(`^[a-z0-9]+$`)

// ImportVPN imports a plugin VPN file (serviceKind "openvpn" in v1) through
// nmcli, since the importers live in the plugins' editor libraries and are not
// on D-Bus, then scopes the new profile to the current user.
func (c *Client) ImportVPN(ctx context.Context, serviceKind, path string) (string, error) {
	op := "import " + serviceKind + " " + path
	if !reKind.MatchString(serviceKind) {
		return "", newErr(op, ErrUnsupported, "bad VPN kind")
	}
	if serviceKind != "openvpn" {
		return "", newErr(op, ErrUnsupported, "only openvpn imports are supported in v1")
	}
	before := map[string]bool{}
	for _, s := range c.view().settings {
		before[vStr(s[settingConnection], "uuid")] = true
	}
	cmd := exec.CommandContext(ctx, c.nmcli, "connection", "import", "type", serviceKind, "file", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		kind := error(nil)
		switch {
		case strings.Contains(msg, "not authorized"), strings.Contains(msg, "Insufficient privileges"):
			kind = ErrPermissionDenied
		case strings.Contains(msg, "executable file not found"):
			kind = ErrUnsupported
			msg = "nmcli is not installed: " + msg
		case strings.Contains(msg, "plugin"), strings.Contains(msg, "Unknown VPN type"):
			kind = ErrUnsupported
			msg = "the NetworkManager " + serviceKind + " plugin is not installed: " + msg
		}
		return "", newErr(op, kind, msg)
	}
	uuid := ""
	if m := reUUID.FindStringSubmatch(stdout.String()); m != nil {
		uuid = strings.ToLower(m[1])
	} else {
		// Fall back to the profile that appeared.
		deadline := time.Now().Add(3 * time.Second)
		for uuid == "" && time.Now().Before(deadline) {
			c.resyncConnections(ctx)
			for _, s := range c.view().settings {
				u := vStr(s[settingConnection], "uuid")
				if u != "" && !before[u] {
					uuid = u
					break
				}
			}
			if uuid == "" {
				select {
				case <-ctx.Done():
					return "", newErr(op, nil, ctx.Err().Error())
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
	}
	if uuid == "" {
		return "", newErr(op, nil, "nmcli succeeded but no new profile UUID could be determined: "+strings.TrimSpace(stdout.String()))
	}
	// Make sure the cache knows the profile before we edit it.
	if _, ok := c.view().connByUUID(uuid); !ok {
		c.resyncConnections(ctx)
	}
	if err := c.SetProfilePermissions(ctx, uuid, true); err != nil {
		return uuid, err
	}
	return uuid, nil
}

// newUUID makes a random v4 UUID (NM accepts client-chosen UUIDs).
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
