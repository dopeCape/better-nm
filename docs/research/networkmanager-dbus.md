# NetworkManager D-Bus from Go

Research for [#6](https://github.com/dopeCape/better-nm/issues/6). Verified against
NetworkManager **1.56.0** (the dev machine: NixOS, Wi-Fi `wlp4s0`, `nm-openvpn` 1.12.3
registered) using the interface XML that NM 1.56.0 installs under
`share/dbus-1/interfaces/`, `nm-settings-dbus(5)` 1.56.0, the NM 1.56.0 source, and
live `busctl`/`nmcli` introspection. Citations use `[Nx]`/`[Gx]` tags listed at the end.

## Recommendation

Use **`github.com/godbus/dbus/v5`** directly and write a thin, bnm-owned typed layer
over the ~12 NM interfaces bnm needs. Do **not** build on `Wifx/gonetworkmanager`:
it is a convenience wrapper over the same godbus calls, was last tested against NM
1.40 with "no active development workforce" [G3], and lacks every 1.12+ method bnm
needs most (`AddAndActivateConnection2`, `AddConnection2`, `Update2`,
`GetPermissions`, scan options, `AgentManager`/`SecretAgent`, `Device.WireGuard`)
[G4]. Copying its enum/stringer files (MIT-style licence "NOASSERTION" per GitHub; check
before vendoring) is reasonable. The only other maintained binding,
`linuxdeepin/go-dbus-factory`, is GPL-3.0 and generated from a pre-1.16 XML snapshot [G6].

## 1. Library options

| Library | Version / date | Status | Notes |
|---|---|---|---|
| `godbus/dbus/v5` | v5.2.2, 2025-12-29 [G1] | 1196 stars, pushed 2026-05-01, BSD-2 [G2] | Native protocol impl; `SystemBus`/`SystemBusPrivate`, `AddMatchSignal` with `WithMatchInterface/Member/ObjectPath/PathNamespace/Arg0Namespace`, `Signal(ch)`, `Export`/`ExportMethodTable`/`RequestName` (needed for a secret agent), `prop` and `introspect` subpackages [G7]. README still says "API is considered unstable" [G8]. Go >= 1.20. |
| `Wifx/gonetworkmanager/v3` | v3.2.0, 2025-05-06 [G1] | 121 stars, last push 2025-10-02, 5 open issues/PRs [G2] | Wraps NM root, Settings, Settings.Connection, Device (+Wired/Wireless/Bridge/Dummy/Generic/IpTunnel/Statistics), AccessPoint, ActiveConnection, VpnConnection, Checkpoint, IP4/6Config, DHCP4/6Config, DnsManager [G4]. README: "compatible with NetworkManager 0.9 to 1.40", "Tested with NetworkManager 1.40.0", "no active development workforce from the maintainer" [G3]. go.mod pins godbus v5.1.0 (MVS upgrades cleanly) [G5]. |
| `linuxdeepin/go-dbus-factory` `system/org.freedesktop.networkmanager` | pseudo-version 2026-08-18 [G6] | pushed 2026-08-19, GPL-3.0 [G2] | Generated from bundled XML incl. `AgentManager.xml`, `SecretAgent.xml`; has `Connect*` signal helpers and mocks. Bundled XML has no `Device.WireGuard.xml` (added in NM 1.16), so generated surface predates that [G6]. Licence rules it out for bnm. |
| `BellerophonMobile/gonetworkmanager`, `danilarff86/...`, `AmandaCameron/go.networkmanager` | archived 2019 / fork 2020 / 2013 [G2] | dead | Wifx is the continuation of BellerophonMobile [G2]. |

`gonetworkmanager` gaps that matter for bnm (from its v3.2.0 interface definitions [G4]):
no `AddAndActivateConnection2`, `AddConnection2`, `Update2`, `GetPermissions`,
`CheckPermissions`, `VersionId`; `RequestScan()` always sends an empty options dict
(no `ssids` for hidden networks) [G4]; no AgentManager/SecretAgent; no
`Device.WireGuard`/`Device.Vlan` (PR #51 open); `Subscribe()` is just a
`path_namespace='/org/freedesktop/NetworkManager'` match returning raw `*dbus.Signal`
[G4]; `Device.SubscribeState` registers a channel that receives every matched signal on
the connection, not only that device's (open PR #52 "fix: SubscribeState filters signals
by device path") [G2][G4]. `ConnectionSettings` is `map[string]map[string]interface{}`
with variants recursively unwrapped [G4], which loses D-Bus type information that
`Update`/`Update2` need to re-encode (e.g. `ipv4.dns` is `au`, `ipv6.dns` is `aay`) [N9].

## 2. The D-Bus surface bnm needs (NM 1.56.0)

All confirmed live with `busctl introspect org.freedesktop.NetworkManager ...` [L1].

- `/org/freedesktop/NetworkManager` (`org.freedesktop.NetworkManager`):
  `ActivateConnection(ooo)->o`, `AddAndActivateConnection(a{sa{sv}}oo)->oo`,
  `AddAndActivateConnection2(a{sa{sv}}ooa{sv})->ooa{sv}`, `DeactivateConnection(o)`,
  `GetDeviceByIpIface(s)->o`, `GetPermissions()->a{ss}`, `Enable(b)`, `Reload(u)`,
  Checkpoint*; signals `StateChanged(u)`, `DeviceAdded(o)`, `DeviceRemoved(o)`,
  `CheckPermissions()`; rw props `WirelessEnabled`, `WwanEnabled`, `ConnectivityCheckEnabled`,
  `GlobalDnsConfiguration` [N1].
- `/org/freedesktop/NetworkManager/Settings`: `ListConnections`, `GetConnectionByUuid`,
  `AddConnection`, `AddConnectionUnsaved`, `AddConnection2(a{sa{sv}}ua{sv})->oa{sv}`,
  `ReloadConnections`; signals `NewConnection(o)`, `ConnectionRemoved(o)` [N2].
- `/org/freedesktop/NetworkManager/Settings/N` (`...Settings.Connection`): `GetSettings`
  (never returns secrets), `GetSecrets(s)`, `Update`, `UpdateUnsaved`, `Save`,
  `Update2(a{sa{sv}}ua{sv})->a{sv}`, `Delete`, `ClearSecrets`; signals `Updated`,
  `Removed`; props `Unsaved`, `Flags`, `Filename`, `VersionId` [N3][L1].
- `/org/freedesktop/NetworkManager/Devices/N`: `Device` (`StateChanged(uuu)`, `Disconnect`,
  `Reapply`, `GetAppliedConnection`, `Managed`/`Autoconnect` rw), plus per-type
  `Device.Wireless` (`RequestScan(a{sv})`, `GetAllAccessPoints`, `AccessPoints`,
  `ActiveAccessPoint`, `LastScan`, `AccessPointAdded/Removed`), `Device.Wired`,
  `Device.WireGuard` (`PublicKey`, `ListenPort`, `FwMark`) [N4][N5][N6].
- `/org/freedesktop/NetworkManager/AccessPoint/N`: `Ssid(ay)`, `Strength(y)`, `Frequency`,
  `Flags`/`WpaFlags`/`RsnFlags`, `HwAddress`, `Mode`, `MaxBitrate`, `Bandwidth`, `LastSeen` [N7].
- `/org/freedesktop/NetworkManager/ActiveConnection/N`: `Connection.Active`
  (`StateChanged(uu)`, `State`, `StateFlags`, `Vpn`, `Devices`, `Ip4Config`...) and, for VPNs,
  `VPN.Connection` (`VpnStateChanged(uu)`, `VpnState`, `Banner`) [N8].
- `/org/freedesktop/NetworkManager/AgentManager`: `Register(s)`,
  `RegisterWithCapabilities(su)`, `Unregister()` [N10].
- `/org/freedesktop` implements `org.freedesktop.DBus.ObjectManager`
  (`GetManagedObjects`, `InterfacesAdded`, `InterfacesRemoved`) [L1] - one call loads the
  whole object graph for a startup snapshot.

Property change notification is the standard `org.freedesktop.DBus.Properties.PropertiesChanged`;
NM's legacy per-interface `PropertiesChanged` was deprecated in the 1.6 cycle and removed
in the 1.32 cycle [N11].

## 3. Feature coverage

**Wi-Fi scan.** `Device.Wireless.RequestScan(options)`; the only option is `ssids`
(`aay`) for probing hidden networks. Completion is signalled by `PropertiesChanged` on
`LastScan` (CLOCK_BOOTTIME ms, -1 = never scanned) [N5]. NM 1.56 rejects the call with
`NM_DEVICE_ERROR_NOT_ALLOWED` "Scanning not allowed while unavailable" only when Wi-Fi is
disabled, there is no supplicant interface, or the device state is below DISCONNECTED;
otherwise the request is queued and the kick-off logic defers it if a scan is not allowed
right now (no "too frequent" error) [N12]. Results: `AccessPoints` property /
`GetAllAccessPoints` plus `AccessPointAdded/Removed` [N5]; read `Ssid`, `Strength`,
`RsnFlags`/`WpaFlags`, `Frequency` per AP [N7].

**Wi-Fi connect (new network).** `AddAndActivateConnection2(settings, device, ap_path,
options)` with a partial settings dict; NM completes missing settings from the device and
AP. Options: `persist` = `disk` (default) | `memory` | `volatile`, `bind-activation` =
`none` | `dbus-client` [N1]. Password goes in
`802-11-wireless-security.psk` with `key-mgmt` = `wpa-psk` (WPA2+WPA3 personal) or `sae`
(WPA3 only) [N9] - see section 5. gonetworkmanager exposes only the non-`2` variant
(`AddAndActivateWirelessConnection`) [G4].

**Wi-Fi connect (saved).** `ActivateConnection(profile, device, ap_or_"/")`; `"/"` for
the AP lets NM pick one [N1].

**Forget / saved list.** `Settings.ListConnections` (all exported profile paths, no
ACL filtering at list time [N13]) then `Settings.Connection.GetSettings` per path
(ACL-checked, see section 6) and `Delete` [N3]. Filter by `connection.type` =
`802-11-wireless` and match `802-11-wireless.ssid` (`ay`) [N9].

**Wired.** `ActivateConnection(profile, device, "/")`, or `ActivateConnection("/",
device, "/")` to let NM choose the best profile for the device; `AddAndActivateConnection`
with `{connection:{type:"802-3-ethernet"}}` creates one. `specific_object` is ignored for
wired [N1].

**Per-profile IPv4/IPv6/DNS.** `GetSettings` -> edit `ipv4`/`ipv6` -> `Update2(settings,
0x1 to-disk, {"version-id": VersionId})`. Keys (all from [N9]): `method`
(`auto`|`manual`|`disabled`|`link-local`|`shared`...), `address-data` (`aa{sv}`, each
`{address:s, prefix:u}`), `gateway` (s), `route-data` (`aa{sv}`, `{dest, prefix,
next-hop, metric}`), `dns` (IPv4: `au` network-byte-order; IPv6: `aay`), `dns-search`
(`as`, `~domain` = routing-only), `dns-options`, `dns-priority` (i; negative = exclusive),
`ignore-auto-dns`, `ignore-auto-routes`, `never-default`; IPv6 adds `addr-gen-mode`,
`ip6-privacy`. Gotcha: `GetSettings` also returns the deprecated `addresses`/`routes`
arrays, and "if you send this property the daemon will ignore 'address-data' and
'gateway'" - strip them before `Update2` [N9]. `Update2` flags: `0x1 to-disk`, `0x2
in-memory`, `0x20 block-autoconnect`, `0x40 no-reapply`; `args.version-id` (since 1.44)
rejects the update if the profile changed concurrently [N3]. `Updated` fires on any
settings/permissions change; clients must re-`GetSettings` [N3]. Applying to a live
device is `Device.Reapply` (network-control) [N4][N14].

**WireGuard (NM-native).** Profile with `connection.type=wireguard`,
`connection.interface-name` (software device name), `wireguard.private-key` (base64),
`private-key-flags`, `listen-port`, `fwmark`, `mtu`, `peer-routes`,
`ip4-auto-default-route`/`ip6-auto-default-route` (NMTernary), and `peers` (`aa{sv}`)
[N9][N15]. Peer keys from `nm-setting-wireguard.h`: `public-key`, `endpoint`,
`allowed-ips`, `persistent-keepalive`, `preshared-key`, `preshared-key-flags` [N15].
Addresses/DNS go in `ipv4`/`ipv6` as above; `ipv4.gateway` "usually conflicts with routing
that NetworkManager configures for WireGuard interfaces" [N9]. Create with
`AddConnection2`, activate with `ActivateConnection`; runtime state via
`Device.WireGuard` [N6]. Nothing in gonetworkmanager models WireGuard [G4].

**OpenVPN via `vpn` setting.** `connection.type=vpn`,
`vpn.service-type="org.freedesktop.NetworkManager.openvpn"`, `vpn.data` (string->string),
`vpn.secrets` (string->string), `vpn.user-name`, `vpn.persistent`, `vpn.timeout` [N9]. The
plugin is registered on this machine by `/etc/NetworkManager/VPN/nm-openvpn-service.name`
(`service=org.freedesktop.NetworkManager.openvpn`, `supports-multiple-connections=true`,
`supports-hints=true`) [L2]. `vpn.data` keys come from NetworkManager-openvpn 1.12.3
`shared/nm-service-defines.h`: `connection-type` (`tls` | `password` | `password-tls` |
`static-key`), `remote`, `port`, `proto-tcp`, `ca`, `cert`, `key`, `ta`, `ta-dir`,
`tls-crypt`, `tls-crypt-v2`, `cipher`, `auth`, `comp-lzo`/`compress`, `dev`/`dev-type`,
`username`, `remote-cert-tls`, `verify-x509-name`, proxy keys, and the flag keys
`password-flags`, `cert-pass-flags`, `http-proxy-password-flags`; secrets are `password`,
`cert-pass`, `http-proxy-password`, `challenge-response` [O1]. `remote` is a `", "`-joined
list of `host[:port[:proto]]` (IPv6 hosts bracketed), as composed by the plugin's importer
[O2]. Certificate keys are file paths that `nm-openvpn-service` hands to `openvpn`.
`AddAndActivateConnection(2)` "Cannot be used for VPN connections" [N1]: use
`AddConnection2` then `ActivateConnection(path, "/", "/")` (device is ignored for VPNs;
`specific_object` may be a base ActiveConnection or `"/"`) [N1]. Progress via
`Connection.Active.StateChanged` and `VPN.Connection.VpnStateChanged`/`Banner` [N8].

## 4. Signals for live refresh

| Need | Signal (all standard D-Bus signals) |
|---|---|
| Global state / connectivity / primary connection | `NetworkManager.StateChanged(u)`; `Properties.PropertiesChanged` for `ActiveConnections`, `PrimaryConnection`, `Connectivity`, `WirelessEnabled` [N1] |
| Device list | `DeviceAdded(o)` / `DeviceRemoved(o)` [N1]; or `ObjectManager.InterfacesAdded/Removed` on `/org/freedesktop` [L1] |
| Device state | `Device.StateChanged(new, old, reason)` [N4] |
| AP list | `Device.Wireless.AccessPointAdded/Removed`, `PropertiesChanged` on `LastScan`/`ActiveAccessPoint`, and on each `AccessPoint` (`Strength`, `LastSeen`) [N5][N7] |
| Saved profiles | `Settings.NewConnection/ConnectionRemoved`; `Settings.Connection.Updated/Removed` [N2][N3] |
| Activation progress | `Connection.Active.StateChanged(state, reason)`; `VPN.Connection.VpnStateChanged` [N8] |
| Permission changes | `CheckPermissions` -> re-call `GetPermissions` [N1] |

With godbus: one `AddMatchSignal(dbus.WithMatchPathNamespace("/org/freedesktop/NetworkManager"))`
plus one for the ObjectManager on `/org/freedesktop`, then `conn.Signal(ch)` [G7]. The
default signal handler never drops: when `ch` is full it spawns a goroutine per signal,
which breaks ordering; use `dbus.WithSignalHandler(dbus.NewSequentialSignalHandler())`
for in-order delivery [G9]. `Signal` bodies carry raw `dbus.ObjectPath`/`uint32`; bnm's
typed layer decodes them.

## 5. Secrets

- Every secret has a `<name>-flags` bitfield: `0x0` none = system-owned (NM stores it,
  root-only plaintext via the keyfile plugin), `0x1 agent-owned` (a session secret agent
  stores/provides it), `0x2 not-saved` (ask every time), `0x4 not-required` [N9][N16]. For
  `vpn`, flags live in `vpn.data` as `"<secret>-flags"` [N17].
- **Supplying a Wi-Fi password without an agent**: put `psk` in the settings passed to
  `AddAndActivateConnection2`/`AddConnection2`/`Update2` and leave `psk-flags` at `0`.
  "Secrets may be part of the update request, and will be either stored in persistent
  storage or sent to a Secret Agent for storage, depending on the flags associated with
  each secret" [N3]. The existing profile on the dev machine is exactly this
  (`psk-flags:0`) [L3]. No agent is required.
- **When an agent is required**: any secret flagged agent-owned or not-saved, or a missing
  secret at activation time, makes NM ask registered agents; with none registered the
  activation fails with `NM_AGENT_MANAGER_ERROR_NO_SECRETS` "No agents were available for
  this request." [N18]. Desktop applets register agents; a headless/TUI session will not
  have one. So: if bnm wants interactive prompts (wrong password, OTP, VPN password not
  saved), the daemon must implement `org.freedesktop.NetworkManager.SecretAgent`
  (`GetSecrets(a{sa{sv}} o s as u)->a{sa{sv}}`, `CancelGetSecrets`, `SaveSecrets`,
  `DeleteSecrets`) on `/org/freedesktop/NetworkManager/SecretAgent` and call
  `AgentManager.RegisterWithCapabilities(id, 0x1 VPN_HINTS)` [N10][N19]. Identifier
  rules: 3-255 chars of `[A-Za-z0-9_-.]`, unique per user session (e.g.
  `io.github.dopecape.bnm`) [N10]. `GetSecrets` flags: `0x1 ALLOW_INTERACTION`, plus
  `REQUEST_NEW`, `USER_REQUESTED`, `WPS_PBC_ACTIVE` [N20]; VPN hints arrive as key names
  (`username`, `password`, `cert-pass`, `http-proxy-password`) and `x-vpn-message:` text
  [N19][O3]. If an agent returns agent-owned secrets it must persist them itself; NM will
  not call `SaveSecrets` back on that agent [N19].
- `Settings.Connection.GetSecrets` returns only secrets from persistent storage or the
  caller's own session agent, never prompts [N3], and is polkit-gated as a modify
  operation (section 6). NM will only forward system-owned secrets to an agent (with
  interaction allowed) after checking the agent's own `settings.modify.*` permission [N21].

## 6. Polkit gating per operation (NM 1.56.0 source + shipped policy defaults)

| D-Bus operation | polkit action | Shipped default (`allow_any` / `inactive` / `active`) |
|---|---|---|
| `ActivateConnection`, `DeactivateConnection`, `CheckConnectivity`, `Device.Disconnect/Reapply/Delete` | `network-control` [N14][N22] | `auth_admin` / yes / yes [N23] |
| `AddAndActivateConnection(2)` | `network-control`, then `settings.modify.own` or `.system` for the new profile [N22][N24] | as above + below |
| `Device.Wireless.RequestScan` | `wifi.scan` [N12] | `auth_admin` / yes / yes [N23] |
| `Settings.AddConnection*`/`AddConnection2`, `Connection.Update/Update2/Delete/GetSecrets/ClearSecrets` | `settings.modify.own` when `connection.permissions` has exactly one entry (the caller's `user:<name>:`), else `settings.modify.system` [N24][N25] | own: `auth_self_keep` / yes / yes; system: `auth_admin_keep` x3 [N23] |
| `Settings.Connection.GetSettings`, `ListConnections`, property reads | no polkit; `GetSettings` only checks the profile is visible to the caller per `connection.permissions` [N13][N25] | - |
| `WirelessEnabled` / `WwanEnabled` writes | `enable-disable-wifi` / `-wwan` [N26] | - / no / yes [N23] |
| `Enable(b)` | `enable-disable-network` [N22] | - / no / yes |
| `Sleep` | `sleep-wake` | - / no / no |
| `Reload`, Checkpoint*, `SaveHostname`, `GlobalDnsConfiguration` | `reload`, `checkpoint-rollback`, `settings.modify.hostname`, `settings.modify.global-dns` [N22] | `auth_admin_keep` x3 |
| `ActivateConnection` on a profile with `permissions` set | caller must be in the ACL, independent of polkit [N27] | - |

Consequences for an unprivileged user-session daemon: in an *active local session* the
defaults let it scan, activate/deactivate, toggle Wi-Fi, and create/modify/delete its own
`user:<name>:`-scoped profiles without prompts; system-wide profiles (the default when
`connection.permissions` is empty) need `auth_admin_keep` [N23]. NM makes these calls
with `allow_interaction=TRUE`, so the D-Bus call blocks while polkit prompts through the
session's polkit agent; without one it fails with `PermissionDenied` [N22]. bnm should
call `GetPermissions` (values `yes`/`auth`/`no`) at startup and on `CheckPermissions` to
gate UI [N1]. Note the dev machine reports `yes` for all 17 permissions [L4], so
prompting behaviour must be tested on a stock policy elsewhere.

## 7. Gaps that need raw D-Bus (with gonetworkmanager) or are not on D-Bus at all

1. Everything in section 1's gap list: `*2` methods, `GetPermissions`, `RequestScan`
   options, `VersionId`, WireGuard, agent registration and the `SecretAgent` export [G4].
2. Secret agent: no Go binding exists outside GPL code; implement with godbus `Export` +
   `RequestName`-less registration via `AgentManager` [G7][N10].
3. `.ovpn` import/export lives in the C editor plugin (`libnm-openvpn-properties`,
   `import-export.c`), loaded by libnm/nmcli, not exposed on D-Bus [O2][O4]. bnm must exec
   `nmcli connection import type openvpn file ...` or port the parser.
4. Typed encoding of settings dicts (`a{sa{sv}}` with `au`/`aay`/`aa{sv}` leaves) - NM
   rejects wrongly-typed variants; bnm's layer must keep signatures per key [N9].
5. Default polkit prompts need a session polkit agent; bnm's daemon cannot answer them
   itself [N22][N23].

## 8. Notes for the daemon-API and VPN-model tickets

- Model profiles as the raw `a{sa{sv}}` plus `VersionId`, and pass `version-id` to
  `Update2` for optimistic concurrency [N3][L1].
- Decide the ownership policy up front: `user:<name>:` in `connection.permissions` makes
  edits prompt-free (`modify.own`) but hides the profile from other users; empty
  permissions means `auth_admin_keep` per change [N24][N23].
- VPN model: a `vpn` profile is `service-type` + opaque `data`/`secrets` string maps; the
  key vocabulary is plugin-specific and lives in the plugin's `nm-service-defines.h`
  [O1]. WireGuard is a first-class NM type with typed peers, not a `vpn` profile [N15].
- Secrets: system-owned (`flags=0`) needs no agent; anything interactive needs the daemon
  to be a secret agent with `VPN_HINTS` [N18][N19].

## Sources

NM 1.56.0 installed interface XML, `/nix/store/cril7...-networkmanager-1.56.0/share/dbus-1/interfaces/` (also published at <https://networkmanager.dev/docs/api/latest/>):
[N1] `org.freedesktop.NetworkManager.xml` · [N2] `...Settings.xml` · [N3] `...Settings.Connection.xml` ·
[N4] `...Device.xml` · [N5] `...Device.Wireless.xml` · [N6] `...Device.WireGuard.xml` · [N7] `...AccessPoint.xml` ·
[N8] `...Connection.Active.xml`, `...VPN.Connection.xml` · [N10] `...AgentManager.xml` · [N19] `...SecretAgent.xml`.
[N9] `nm-settings-dbus(5)` from NetworkManager 1.56.0 (`man nm-settings-dbus`), sections connection, 802-11-wireless(-security), ipv4, ipv6, wireguard, vpn, "Secret flag types".
NM 1.56.0 source, <https://gitlab.freedesktop.org/NetworkManager/NetworkManager/-/tree/1.56.0>:
[N11] `NEWS` ("Overview of changes since NetworkManager-1.4": deprecate; "...since 1.30": "API CHANGE: D-Bus: remove long deprecated PropertiesChanged signal") ·
[N12] `src/core/devices/wifi/nm-device-wifi.c` `_nm_device_wifi_request_scan` (NOT_ALLOWED check, `NM_AUTH_PERMISSION_WIFI_SCAN`), `_scan_kickoff` ·
[N13] `src/core/settings/nm-settings.c` `impl_settings_list_connections` ·
[N14] `src/core/devices/nm-device.c` `impl_device_reapply/disconnect/delete` (`NM_AUTH_PERMISSION_NETWORK_CONTROL`) ·
[N15] `src/libnm-core-public/nm-setting-wireguard.h` (`NM_SETTING_WIREGUARD_*`, `NM_WIREGUARD_PEER_ATTR_*`) ·
[N16] `src/libnm-core-public/nm-setting.h` `NMSettingSecretFlags` · [N17] `src/libnm-core-impl/nm-setting-vpn.c` `get_secret_flags` (`"%s-flags"`) ·
[N18] `src/core/settings/nm-agent-manager.c` ("No agents were available for this request.") · [N20] `src/libnm-core-public/nm-dbus-interface.h` `NMSecretAgentGetSecretsFlags`, `NMSecretAgentCapabilities` ·
[N21] `nm-agent-manager.c` `_con_get_request_start` ("checking agent ... for MODIFY") ·
[N22] `src/core/nm-manager.c` `impl_manager_*` / `nm_auth_chain_add_call(chain, PERM, TRUE)`; `src/core/nm-active-connection.c` `nm_active_connection_authorize` (`NETWORK_CONTROL`) ·
[N23] `share/polkit-1/actions/org.freedesktop.NetworkManager.policy` as installed by NM 1.56.0 ·
[N24] `nm-settings.c` `nm_settings_add_connection_dbus` (num_permissions == 1 -> `MODIFY_OWN`); `nm-manager.c` `_add_and_activate_auth_done` -> `nm_settings_add_connection_dbus` ·
[N25] `src/core/settings/nm-settings-connection.c` `get_update_modify_permission`, `get_modify_permission_basic`, `impl_settings_connection_get_settings` (`auth_start(..., NULL, ...)`) ·
[N26] `nm-manager.c` property table (`WirelessEnabled` -> `NM_AUTH_PERMISSION_ENABLE_DISABLE_WIFI`) · [N27] `nm-manager.c` `validate_activation_request` (`nm_auth_is_subject_in_acl_set_error`).
NetworkManager-openvpn 1.12.3, <https://gitlab.gnome.org/GNOME/NetworkManager-openvpn/-/tree/1.12.3>:
[O1] `shared/nm-service-defines.h` · [O2] `properties/import-export.c` (remote composition, `do_import`) · [O3] `src/nm-openvpn-service.c` `handle_auth` hints · [O4] `properties/nm-openvpn-editor-plugin.c`.
Go: [G1] proxy.golang.org `.info` for `github.com/Wifx/gonetworkmanager/v3@v3.2.0`, `github.com/godbus/dbus/v5@v5.2.2` ·
[G2] GitHub API `repos/{Wifx/gonetworkmanager, linuxdeepin/go-dbus-factory, godbus/dbus, BellerophonMobile/gonetworkmanager, danilarff86/gonetworkmanager, AmandaCameron/go.networkmanager}` and `Wifx/gonetworkmanager/issues?state=open`, 2026-09-07 ·
[G3] gonetworkmanager v3.2.0 `README.md` · [G4] v3.2.0 `NetworkManager.go`, `Settings.go`, `Connection.go`, `Device.go`, `DeviceWireless.go`, `utils.go` · [G5] v3.2.0 `go.mod` ·
[G6] <https://pkg.go.dev/github.com/linuxdeepin/go-dbus-factory/system/org.freedesktop.networkmanager> and the repo's `system/org.freedesktop.networkmanager/` XML listing ·
[G7] godbus v5.2.2 `conn.go`, `export.go`, `match.go`, `prop/`, `introspect/` · [G8] godbus `README.md` · [G9] godbus `default_handler.go` `signalChannelData.deliver`, `sequential_handler.go`.
Local: [L1] `busctl introspect org.freedesktop.NetworkManager {/org/freedesktop, /org/freedesktop/NetworkManager, .../Settings, .../Settings/1, .../Devices/4 (wlp4s0), .../AgentManager}`, `Version` = 1.56.0 ·
[L2] `/etc/NetworkManager/VPN/nm-openvpn-service.name` · [L3] `nmcli -f 802-11-wireless-security.psk-flags connection show <wifi profile>` = 0 · [L4] `nmcli general permissions`.
