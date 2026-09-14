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
| `POST /v1/wifi/connect` | `core.ConnectWifiRequest` `{ssid, device?, password?, hidden?, username?}` | ok. Without `password` on a secured, unknown network the profile is created without the secret and NetworkManager asks for it through the [secret agent](#secrets-networkmanager-password-prompts); the call then blocks until the prompt is answered (or cancelled / expired, then 400) |
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
| `secret-needed` | NetworkManager asked bnmd (its secret agent) for a secret the profile does not store; `data.request_id` names the `core.SecretRequest` to answer, `data.connection_uuid`, `data.connection_name`, `data.ssid` (Wi-Fi) and `data.vpn` (`"true"`) say what for | `Password needed for <name>` / `<field labels> for <network> (the previous one was rejected). <plugin message> run: bnm secrets` |
| `secret-resolved` | that request ended; `data.request_id`, `data.outcome` = `answered` \| `cancelled` \| `timeout` | `Password prompt answered` / – |

A slow stream consumer never blocks the daemon: it is buffered (128 items) and drops the newest
item when full.

## Secrets (NetworkManager password prompts)

bnmd registers with NetworkManager as the session's secret agent
(`org.freedesktop.NetworkManager.SecretAgent`, identifier `io.github.dopecape.bnm`). When an
activation needs a secret the profile does not hold (a wrong Wi-Fi password being retried, an
OTP / challenge-response, a VPN password saved as agent-owned or not-saved) NM calls the agent,
the daemon emits `secret-needed` and keeps a `core.SecretRequest` open for two minutes; the
activation call (`POST /wifi/connect`, `POST /profiles/{uuid}/activate`, `POST /vpn/{id}/connect`)
keeps waiting while the prompt is open. A surface answers or cancels it here.

| Route | Request | Response |
|---|---|---|
| `GET /v1/secrets` | – | `[]core.SecretRequest` still open, oldest first |
| `GET /v1/secrets/{id}` | – | `core.SecretRequest` (404 once answered, cancelled or expired) |
| `POST /v1/secrets/{id}` | `core.SecretAnswer` `{secrets: {<field key>: <value>}, save}` | ok; 404 when the request is gone, 409 when it was already answered or cancelled, 400 when no requested key is present |
| `POST /v1/secrets/{id}/cancel` | – | ok (the activation then fails with `no secrets available`); 404 / 409 as above |

`core.SecretRequest`: `{id, connection_uuid, connection_name, ssid?, vpn, vpn_kind?, setting_name,
fields: [{key, label, secret}], message?, request_new, user_requested, created_at, expires_at}`.
`fields` are the values NM asks for (`psk` "Wi-Fi password", `wep-key0`, `password`, `cert-pass`,
`http-proxy-password`, `challenge-response` "One-time code / challenge response", `username`
(not secret), ...), `message` is free text from the VPN plugin, `request_new` means the stored
secret was tried and rejected (say "wrong password"). With `save` true the secret is stored in
the profile as system-owned (`<key>-flags 0`) so the next activation does not ask: NM does that
itself for secrets the profile already calls system-owned, and bnmd rewrites the profile for
agent-owned / not-saved ones (imported `.ovpn` files default to agent-owned). All routes answer
501 when the daemon has no secret agent (`bnmd --fake` has an in-memory one that prompts when a
secured unknown network is joined without a password, or with the password `wrong`, and when
the fake OpenVPN profile is connected).

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
| `GET /v1/config` | – | the whole `config.Config` (sections `monitor`, `notify`, `speed`, `tailscale`, `daemon`); `notify.secret_needed` (default on) is the desktop notification for password prompts, never debounced, rate-limited or muted |
| `PUT /v1/config` | `{key, value}` with a dotted key such as `notify.degraded`; lists are comma separated, durations Go syntax (`30s`) | the new `config.Config` (persisted to `$XDG_CONFIG_HOME/bnm/config.toml`) |
| `POST /v1/notify/test` | – | ok (sends a test desktop notification; not recorded in history) |

## Versioning

`GET /v1/status` carries `version` (the daemon build) and `api_version` (`1`). Clients refuse a
daemon whose `api_version` differs from theirs and tell the user to restart or update.
