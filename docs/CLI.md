# bnm command line

Decided in [issue #15](https://github.com/dopeCape/better-nm/issues/15). `bnm` is a thin client of
`bnmd` (see [API.md](API.md)): every command maps onto one or a few API calls, renders a table or a
one-screen summary for humans, and prints the raw daemon structs with `--json`. The code lives in
`internal/cli`; `cmd/bnm` only wires signals and the exit code.

## Global flags and behaviour

```
--json           print the raw daemon structs as JSON (every command that prints data)
--no-color       disable colour; the NO_COLOR environment variable does the same
--socket PATH    bnmd socket (default $XDG_RUNTIME_DIR/bnm/bnmd.sock, or $BNM_SOCKET)
--no-autostart   do not start bnmd when it is not running
-q, --quiet      print nothing but errors for actions
```

- **Daemon**: the first call connects to the socket and, when nothing answers, starts `bnmd`
  detached (found next to `bnm`, then in `PATH`, or `$BNM_DAEMON`) and prints `started bnmd` on
  stderr. `--no-autostart` turns that into exit 3.
- **Colour** is on only for a terminal (or when `CLICOLOR_FORCE` is set) and never when `NO_COLOR` or
  `--no-color` is given. Tables stay within 80 columns; a free-text column shrinks when needed.
  Markers: `●` green connected/ok, yellow connecting/learning, red error/degraded, `○` dim
  disconnected; `✓` saved; signal as `▂▄▆█` plus a percentage.
- **Names**: every `<name|uuid>` argument accepts a profile name, an SSID, a VPN name, an exact
  ID/UUID, a UUID prefix (3+ characters) or a unique name prefix, in that order. More than one match
  is an error that lists the candidates; `ts` is an alias for `tailscale`.
- **Errors** go to stderr as `error: <message>` and, when the daemon attached one, `hint: <what to
  do>`. Nothing is printed on stdout in that case.
- **Exit codes**: `0` ok, `1` the daemon or the CLI refused, `2` usage (bad flag or argument; the
  usage line follows), `3` bnmd unreachable or speaking another API version, `4` polkit or
  operator denied.
- **Actions** (`connect`, `up`, `set`, ...) print one confirmation line; `-q` silences it and
  `--json` turns it into `{"ok":true,"message":...}` (or the resulting object where there is one).

## Command tree

```
bnm                          terminal UI (help when the TUI is not built in)
bnm tui                      terminal UI
bnm status                   one-screen summary
bnm devices                  network interfaces (physical, then virtual with their owner)
bnm wifi list [--rescan] [--device D]
bnm wifi connect <ssid> [--password P | --ask] [--hidden] [--device D]
bnm wifi disconnect [--device D]
bnm wifi forget <ssid|uuid>
bnm wifi on | off
bnm wifi saved
bnm wired list | up [profile] [--device D] | down [--device D]
bnm profile list [--type T] | show <name|uuid> | ip <name|uuid> ... | autoconnect <name|uuid> on|off | delete <name|uuid>
bnm vpn list | up <name|id> | down <name|id> | toggle <name|id>
bnm vpn add wg <file.conf> [--name N] | add ovpn <file.ovpn> [--name N]
bnm vpn ts status | peers [--all] | exit-node [list | set <peer> [--allow-lan] | off | on] | login | logout | dns on|off
bnm monitor [status] | history [--anchor A] [--key K] [--limit N] | baseline [--reset] [--key K] | pause | resume
bnm speed [--quick] [--provider cloudflare|iperf3|librespeed] [--server S]
bnm speed history [--key K] [--limit N]
bnm events [--limit N] | events --follow [--changes] [--limit N]
bnm diag devices [--device D] [--no-sweep] | ports | routes [--all] | dns <name> [--server S] [--type T] | public-ip | infra
bnm config | get <key> | set <key> <value> | mute <network-key> | unmute <network-key> | keys | path
bnm notify test
bnm daemon status | start | stop | restart | install [--user-unit] | uninstall | logs [-f] [-n N]
bnm version
```

## Commands

### `bnm status`

Everything at a glance: the primary connection (name, type, device), Wi-Fi signal/band/security
when on Wi-Fi, IPv4/IPv6, gateway, DNS, NM's connectivity verdict, which VPNs are up, the monitor's
verdict with the current round-trip per anchor, and the daemon build, uptime and NM version.

```
● Connected to ALHN-F832-5 (wifi via wlp4s0)
  Wi-Fi     ▂▄▆█ 75%  5 GHz ch 161  WPA2
  IPv4      192.168.1.73/24
  IPv6      fe80::d2d7:48d6:a423:cc14/64
  Gateway   192.168.1.254
  DNS       192.168.1.254
  Internet  full
  VPN       Tailscale
  Monitor   learning  gateway 1.7 ms · 1.1.1.1 16 ms · 8.8.8.8 15 ms
  Daemon    bnmd 21286d2  up 8s  NM 1.56.0
```

`--json` prints `{"status": <GET /status>, "vpns": <GET /vpn>, "monitor": <GET /monitor>}`.

### `bnm wifi`

| Command | What it does |
|---|---|
| `list [--rescan] [--device D]` | SSIDs in range sorted active first, then by signal: marker, SSID, `▂▄▆█` bars and %, band/channel, security (`open`, `OWE`, `WEP`, `WPA2`, `WPA3`, `802.1X`), `connected`/`saved`. `--rescan` requests a scan and waits (up to 6 s) for the results. |
| `connect <ssid> [--password P \| --ask] [--hidden] [--device D]` | Joins the network; NM creates the profile when there is none. A secured, unknown network prompts for the password on a terminal (`--ask` forces the prompt; it never echoes); without a terminal it is an error with the hint to pass `--password` or `--ask`. An SSID that is not in range (after one rescan) and has no saved profile is refused unless `--hidden` is given, because NM would otherwise create and keep a profile for it. |
| `disconnect [--device D]` | Brings the Wi-Fi device down. |
| `forget <ssid\|uuid>` | Deletes the saved Wi-Fi profile. |
| `on` / `off` | Toggles the Wi-Fi radio. |
| `saved` | Saved Wi-Fi profiles: SSID, security, autoconnect, last used, UUID prefix. |

```
   SSID         SIGNAL       BAND        SECURITY
●  ALHN-F832-5  ▂▄▆█    82%  5 GHz/161   WPA2      connected
✓  ALHN-F832    ▂▄▆█    80%  2.4 GHz/11  WPA2      saved
```

### `bnm wired`

`list` shows ethernet devices (state, link speed, IPv4, active profile) and wired profiles.
`up [profile]` activates the named wired profile, or the only one; with several it asks you to name
one. `down` disconnects the connected ethernet device.

### `bnm profile`

| Command | What it does |
|---|---|
| `list [--type wifi\|ethernet\|wireguard\|vpn\|bridge]` | Every saved profile, active first: name, type, interface, autoconnect, last used, UUID prefix. |
| `show <name\|uuid>` | The whole profile: IP layers, permissions, file, VPN plugin data (never secrets), WireGuard peers. |
| `ip <name\|uuid> --ipv4-method M [--address CIDR]... [--gateway G] [--dns X]... [--dns-search D]... [--ignore-auto-dns]` and the `--ipv6-method`, `--address6`, `--gateway6`, `--dns6`, `--dns-search6`, `--ignore-auto-dns6` twins | Replaces one or both IP layers; a family whose method is not given is left as is. `manual` needs at least one address. Methods: `auto`, `manual`, `disabled`, `link-local`, `shared` (v4), `ignore` (v6). |
| `autoconnect <name\|uuid> on\|off` | Toggles autoconnect. |
| `delete <name\|uuid>` | Deletes the profile. |

### `bnm vpn`

`list` shows every VPN of every backend with the unified state and a detail column (self IP or
tailnet for Tailscale, peer endpoint for WireGuard, gateway for plugin VPNs, plus any error or hint
the backend attached):

```
   NAME       KIND       BACKEND    STATE         DETAIL
●  Tailscale  Tailscale  tailscale  connected     100.64.0.10 · Tailscale is r…
○  tejas      OpenVPN    nm-vpn     disconnected  vpn.office.example:1194
```

`up`, `down` and `toggle` act on one VPN and wait (up to 20 s) for it to leave `connecting`, then
print the final state; a login URL is printed when the backend needs one. `add wg <file.conf>`
imports a wg-quick file as an NM WireGuard profile and `add ovpn <file.ovpn>` an OpenVPN file through
the NM plugin; the file is read locally and sent inline, so relative paths work. `--name` overrides
the profile name (default: the file name without its extension).

`vpn ts` (alias `tailscale`): `status` (node, IPs, tailnet, MagicDNS, control URL, exit node, DNS,
operator, health), `peers [--all]` (peers not seen in 30 days are hidden unless `--all`),
`exit-node` (current), `exit-node list` (peers offering one), `exit-node set <peer> [--allow-lan]`,
`exit-node off` / `on`, `login` (prints the URL to open), `logout`, `dns on|off`.

### `bnm monitor`

`monitor` (or `monitor status`) prints the network key, the verdict (`learning`, `ok`, `degraded`,
`idle`, `paused`), the probe interval, and one row per anchor: state, current RTT, baseline RTT,
loss, DNS time and sample count. `history` lists recent samples newest last (`--anchor`, `--key`,
`--limit`, default 40). `baseline` shows the baselines; `--reset` forgets them for the current network
(or `--key`). `pause` and `resume` stop and restart probing.

### `bnm speed`

Runs one bandwidth test now (never in the background) and shows a live progress bar per phase on a
terminal (one line per phase otherwise), then:

```
Speed test via cloudflare quick
  Download  18.2 Mbps
  Upload    20.9 Mbps
  Latency   19 ms  jitter 1.1 ms
  Moved     28.1 MB in 14s
  Network   wifi:ALHN-F832-5
```

`--quick` moves about a tenth of the bytes; `--provider` and `--server` override the config. Only
one test runs at a time (a second one is refused). `speed history [--key K] [--limit N]` lists past
results.

### `bnm events`

Without flags: stored events, oldest first (`--limit`, default 100): time, type, network key, title
and body. `--follow` streams live events until interrupted (exit 0); `--changes` also prints the
coarse change hints and `--limit N` stops after N items. With `--json` each item is one JSON line
(`{"change":...}` or `{"event":...}`).

### `bnm diag`

| Command | What it does |
|---|---|
| `devices [--device D] [--no-sweep]` (alias `lan`) | Neighbours on the subnet of the primary device (or `--device`): IP, MAC, hostname, state; `▲` marks the gateway and `●` this host. `--no-sweep` skips the active probe. |
| `ports` | Listening TCP / bound UDP sockets; `*` means every interface; processes of other users show as `(not visible)`. |
| `routes [--all]` | The main routing table; `--all` adds policy tables (VPNs). |
| `dns <name> [--server S] [--type T]` | Resolves and times a lookup (`A` by default). |
| `public-ip` | What the internet sees, via Cloudflare's trace: IP, country, colo. |
| `infra` | Container and VM bridges with their members and neighbours, attributed to docker, podman, libvirt, incus, nspawn or k8s. |

### `bnm config`

`config` prints every setting grouped by section as `key = value`; `get <key>` one value; `set <key>
<value>` changes one through the daemon (persisted to `$XDG_CONFIG_HOME/bnm/config.toml` and applied
at once; lists are comma separated, durations use Go syntax such as `30s`). `mute <network-key>` and
`unmute <network-key>` edit `notify.muted_networks`; a bare SSID of a saved network is accepted for
`wifi:<ssid>`. `keys` lists every key with its default, `path` the file path.

### `bnm notify test`

Sends a test desktop notification through the daemon.

### `bnm daemon`

| Command | What it does |
|---|---|
| `status` | Reachable? Version, API version, uptime, pid (from `bnmd.pid` beside the socket) and the systemd user unit state. Exit 3 when not running. |
| `start` | Starts `bnmd` detached (stdio to `$XDG_STATE_HOME/bnm/bnmd.log`), or `systemctl --user start bnmd` when the unit is installed. |
| `stop` | SIGTERM to the pid, or `systemctl --user stop bnmd` when it runs as a service; waits up to 5 s. |
| `restart` | `stop` then `start`. |
| `install [--user-unit]` | Writes `~/.config/systemd/user/bnmd.service` with `ExecStart=` the absolute path of `bnmd` (next to `bnm`, in `PATH`, or `$BNM_DAEMON`), stops a hand-started daemon, then `systemctl --user daemon-reload` and `enable --now bnmd`. `--user-unit` only writes the file. Reports when `systemctl` is missing. |
| `uninstall` | `systemctl --user disable --now bnmd`, removes the unit, `daemon-reload`. |
| `logs [-f] [-n N]` | `journalctl --user -u bnmd` when the service is active, otherwise the last N lines of `$XDG_STATE_HOME/bnm/bnmd.log`; `-f` follows. |

### `bnm version`

`bnm <version> (api vN)` and the daemon's version when one is reachable (no auto-start).

## The TUI hook

`internal/cli` exposes `RunTUI func(ctx context.Context, c *client.Client) error`. `cmd/bnm` sets it
to the TUI's entry point when the TUI is built in; a bare `bnm` and `bnm tui` call it with a
connected client. With the hook nil, a bare `bnm` prints the help and `bnm tui` says the TUI is not
built in.

## Testing

`go test -race ./internal/cli/...` runs every command in-process against `internal/api` +
`internal/daemon` over a temp Unix socket with `internal/fake` behind it, asserting on the rendered
text and on the decoded `--json` output: status, Wi-Fi list/connect (including the wrong-password
hint and the polkit exit code), VPN list/up/down/toggle/add and the Tailscale commands, monitor,
speed with progress, event history and `--follow` (bounded by `--limit` and by cancellation), diag,
config get/set/mute, name resolution and its ambiguity error, exit codes and usage errors, colour
under `NO_COLOR` / `CLICOLOR_FORCE` / `--no-color`, and the daemon subcommands that do not need
systemd (`status`, `install --user-unit`, `uninstall`, `logs`).
