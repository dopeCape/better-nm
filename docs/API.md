# bnmd HTTP API (v1)

Decided in [issue #12](https://github.com/dopeCape/better-nm/issues/12): HTTP/1.1 + JSON over a
Unix socket, every path under `/v1`. Same-user only; the socket is the whole auth model.

- **Socket**: `$XDG_RUNTIME_DIR/bnm/bnmd.sock` (fallback `/tmp/bnm-<uid>/bnmd.sock`); directory
  0700, socket 0600, `bnmd.pid` lock file beside it. `BNM_SOCKET` overrides the path.
- **curl**: `curl --unix-socket "$XDG_RUNTIME_DIR/bnm/bnmd.sock" http://bnmd/v1/status`
- **Go client**: `internal/client` (`client.New()` auto-starts `bnmd` when the socket is absent and
  refuses a daemon whose `api_version` differs).
- **Bodies**: JSON, `Content-Type: application/json`; unknown fields are rejected (400). Empty
  bodies are accepted where every field is optional.
- **Success**: `200` with the resource, or `{"ok":true}` for actions.
- **Errors**: `{"error": "...", "hint": "...?", "code": "<kind>"}`

| `code` | HTTP | meaning |
|---|---|---|
| `permission` | 403 | polkit / operator denied; `hint` says the one-time fix |
| `not-found` | 404 | profile, device, VPN, peer, route |
| `unsupported` | 501 | backend not built in / not installed |
| `invalid` | 400 | bad input |
| `conflict` | 409 | already running / not active |
| `unavailable` | 503 | backend daemon not running |
| `internal` | 500 | anything else |

Types below are the JSON forms of `internal/core` structs (field names are their `json` tags).
Lists are never `null`; an empty list is `[]`.

## Status and devices

| Route | Request | Response |
|---|---|---|
| `GET /v1/status` | – | `core.Status` + `version`, `api_version`, `uptime_seconds`, `started`, `snapshot_version` |
| `GET /v1/devices` | – | `[]core.Device` |
| `GET /v1/active` | – | `[]core.ActiveConnection` |

## Wi-Fi

| Route | Request | Response |
|---|---|---|
| `GET /v1/wifi?device=` | `device` optional (all Wi-Fi devices when empty) | `[]core.WifiNetwork` |
| `POST /v1/wifi/scan` | `{device?}` | ok (returns when the scan is *requested*; results arrive as a `wifi` change) |
| `POST /v1/wifi/connect` | `core.ConnectWifiRequest` `{ssid, device?, password?, hidden?, username?}` | ok |
| `POST /v1/wifi/disconnect` | `{device?}` (first Wi-Fi device when empty) | ok |
| `POST /v1/wifi/forget` | `{uuid}` | ok |
| `POST /v1/wifi/enabled` | `{on}` | ok |

## Profiles

| Route | Request | Response |
|---|---|---|
| `GET /v1/profiles` | – | `[]core.Profile` |
| `GET /v1/profiles/{uuid}` | – | `core.Profile` |
| `PUT /v1/profiles/{uuid}/ip` | `{ipv4?: core.IPConfig, ipv6?: core.IPConfig}` (a missing family is left as is; at least one required) | ok |
| `POST /v1/profiles/{uuid}/activate` | `{device?}` | ok |
| `POST /v1/profiles/{uuid}/deactivate` | – | ok (409 when not active) |
| `POST /v1/profiles/{uuid}/autoconnect` | `{on}` | ok |
| `DELETE /v1/profiles/{uuid}` | – | ok |

## VPN

| Route | Request | Response |
|---|---|---|
| `GET /v1/vpn` | – | `[]core.VPN` (every backend, unified state machine) |
| `POST /v1/vpn/{id}/connect` | – | ok |
| `POST /v1/vpn/{id}/disconnect` | – | ok |
| `POST /v1/vpn/import` | `{kind: "wireguard"\|"openvpn", name?, path? \| content?}` (exactly one of `path` on the daemon's host or inline `content`; `name` required for inline WireGuard) | `{id, uuid, name, kind, vpn?: core.VPN}` |
| `POST /v1/vpn/tailscale/exit-node` | `{peer, allow_lan}` (`peer` is an ID or name; `""` clears) | ok |
| `POST /v1/vpn/tailscale/exit-node/enabled` | `{on}` | ok |
| `POST /v1/vpn/tailscale/login` | – | `{url}` to open in a browser |
| `POST /v1/vpn/tailscale/logout` | – | ok |
| `POST /v1/vpn/tailscale/accept-dns` | `{on}` | ok |

Tailscale routes answer 501 when no Tailscale adapter is available.

## Monitor

| Route | Request | Response |
|---|---|---|
| `GET /v1/monitor` | – | `core.MonitorStatus` |
| `GET /v1/monitor/samples?key=&anchor=&limit=` | `key` defaults to the current network, `anchor` to all, `limit` to 200 | `[]core.Sample`, newest last |
| `POST /v1/monitor/baseline/reset` | `{key?}` (current network when empty) | ok |
| `POST /v1/monitor/pause` | – | ok |
| `POST /v1/monitor/resume` | – | ok |

## Speed test

| Route | Request | Response |
|---|---|---|
| `POST /v1/speed` | `core.SpeedOptions` `{provider?, server?, quick?, max_bytes?, network_key?}` (defaults from config) | SSE stream: `event: progress` (`core.SpeedProgress`) repeated, then `event: result` (`core.SpeedResult`) or `event: error` (error body) |
| `POST /v1/speed?wait=1` | same | plain JSON `core.SpeedResult` |
| `GET /v1/speed/history?key=&limit=` | `limit` defaults to 50 | `[]core.SpeedResult`, newest last |

Only one test runs at a time; a second request gets 409.

## Events

| Route | Request | Response |
|---|---|---|
| `GET /v1/events?limit=` | `limit` defaults to 100 | `[]core.Event`, oldest first |
| `GET /v1/events/stream` | – | SSE, see below |

The stream is `text/event-stream`. It opens with the comment `: connected`, sends `: ping` every
15 s, and otherwise carries two event types:

```
event: change
data: {"kind":"active","path":"/org/freedesktop/NetworkManager/ActiveConnection/3"}

event: event
data: {"time":"...","type":"disconnected","network_key":"wifi:HomeNet","title":"Disconnected from HomeNet","body":"No network connection","urgency":"normal","data":{"uuid":"..."}}
```

`change` is a `core.Change`: an invalidation hint (`status`, `devices`, `wifi`, `profiles`,
`active`, `vpn`, `monitor`); re-read what you show. `event` is a `core.Event` as derived by the
daemon (see the notification policy in [issue #25](https://github.com/dopeCape/better-nm/issues/25)):

| type | when | title / body |
|---|---|---|
| `connected` | the primary Active Connection becomes activated (a new network, or the first) | `Connected to <name>` / `<ip> via <device>` |
| `disconnected` | the primary goes away and nothing replaces it within 5 s (Wi-Fi roaming and reconnects are dropped) | `Disconnected from <name>` |
| `no-internet` | connectivity stays `portal`/`limited`/`none` for 10 s while connected | `No internet on <name>` / portal: "needs a login page" |
| `internet-restored` | connectivity returns to `full` after a `no-internet` | `Internet restored` |
| `vpn-up` / `vpn-down` | a VPN enters / leaves `connected` (per VPN `id`) | `<VPN> connected` / `<VPN> disconnected` |
| `degraded` / `recovered` | forwarded from the monitor's baseline engine | as sent |

A slow stream consumer never blocks the daemon: it is buffered (128 items) and drops the newest
item when full.

## Diagnostics

| Route | Query | Response |
|---|---|---|
| `GET /v1/diag/lan?device=&sweep=` | `sweep=1` actively probes the subnet | `[]core.LANHost` |
| `GET /v1/diag/ports` | – | `[]core.ListeningPort` |
| `GET /v1/diag/routes` | – | `[]core.Route` |
| `GET /v1/diag/dns?name=&server=&type=` | `name` required; `type` defaults to `A` | `core.DNSAnswer` |
| `GET /v1/diag/public-ip` | – | `core.PublicIP` |
| `GET /v1/diag/infra` | – | `[]core.InfraNetwork` |

## Config and notifications

| Route | Request | Response |
|---|---|---|
| `GET /v1/config` | – | the whole `config.Config` (sections `monitor`, `notify`, `speed`, `tailscale`, `daemon`) |
| `PUT /v1/config` | `{key, value}` with a dotted key such as `notify.degraded`; lists are comma separated, durations Go syntax (`30s`) | the new `config.Config` (persisted to `$XDG_CONFIG_HOME/bnm/config.toml`) |
| `POST /v1/notify/test` | – | ok (sends a test desktop notification; not recorded in history) |

## Versioning

`GET /v1/status` carries `version` (the daemon build) and `api_version` (`1`). Clients refuse a
daemon whose `api_version` differs from theirs and tell the user to restart or update.
