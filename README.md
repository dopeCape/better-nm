# bnm

A NetworkManager front end that doesn't suck. One unprivileged daemon, three surfaces.

- **`bnm`**: a CLI with a full command tree and `--json` everywhere.
- **`bnm`** with no arguments: a terminal UI that refreshes live (the nmtui you wanted).
- **`bnm-desktop`**: a desktop app (Tauri 2: Rust shell + React, WebKitGTK; Wayland and X11) with an optional tray icon.

What it does that nmcli and nmtui don't:

- **One VPN list** across Tailscale, WireGuard and every NetworkManager plugin VPN (OpenVPN, strongSwan, L2TP, ...), with the same connect/disconnect and state for all of them. Tailscale exit nodes, login and MagicDNS are in the same place. Import a wg-quick `.conf` or an `.ovpn` in one command.
- **Continuous quality monitoring**: latency, loss and DNS time to your gateway and two public anchors every 30 s, stored per network, with a per-network baseline. It tells you when *this* network is slower than it usually is, not when it is slower than some absolute number.
- **A speed test** (`bnm speed`) against Cloudflare, or your own iperf3 server. Never runs in the background.
- **Notifications** for connected, disconnected, no internet (captive portal), internet back, VPN up/down, and optionally degradation.
- **Diagnostics**: devices on your LAN, listening ports, routes, DNS lookups, public IP, and Docker/Podman/libvirt bridge networks.

Everything runs as your user: NetworkManager over D-Bus with polkit, Tailscale through its local socket, probes on unprivileged ping sockets. No root, no sudo, ever.

## Install

Prebuilt packages are on the [releases page](https://github.com/dopeCape/better-nm/releases):
`bnm` (CLI + daemon) as deb, rpm, Arch package and static tarballs, and `bnm-desktop` as
AppImage, deb and rpm for x86_64 and aarch64. AUR: `bnm-bin`. The desktop packages depend
on the `bnm` package for the daemon (the AppImage finds `bnmd` on `PATH` or next to itself).

From source, CLI and daemon (Go 1.25):

```
make build            # bin/bnm, bin/bnmd
make install          # copies to ~/.local/bin (plus bin/bnm-desktop and its launcher entry if built)
```

The desktop app is Tauri 2: a Rust shell over a React frontend. It needs rustc/cargo,
node 24, pnpm, and the WebKitGTK 4.1, GTK 3, libayatana-appindicator, librsvg and
libsoup 3 development libraries (`apt install libwebkit2gtk-4.1-dev libgtk-3-dev
libayatana-appindicator3-dev librsvg2-dev libsoup-3.0-dev`; on NixOS everything is in
`nix develop`):

```
make desktop          # bin/bnm-desktop
make desktop-bundle   # AppImage, deb and rpm under desktop/src-tauri/target/release/bundle/
```

Nix: `nix run github:dopeCape/better-nm` for the CLI, `nix build github:dopeCape/better-nm#bnm-desktop`
for the desktop app, or add the flake and enable `services.bnm` (with `services.bnm.desktop = true`
for the app; NixOS module and Home Manager module are both exported).

Run `bnm daemon install` once to run the daemon as a systemd user service. Without it, `bnm` starts the daemon on demand and it keeps collecting baselines in the background.

## Use

```
bnm                          terminal UI
bnm status                   the one-screen summary
bnm wifi list --rescan       networks with signal, band, security
bnm wifi connect "SSID" --ask
bnm vpn list                 every VPN, every backend
bnm vpn up tailscale
bnm vpn ts exit-node set homeserver
bnm vpn add wg ~/mullvad.conf
bnm monitor                  is the network slower than usual?
bnm speed --quick
bnm events --follow
bnm diag devices             who is on my LAN
bnm config set notify.degraded true
```

`bnm --help` has the whole tree; `docs/CLI.md` documents every command.

### Tailscale note

Reads work out of the box. To connect, disconnect or change exit nodes, Tailscale itself requires a one-time `sudo tailscale set --operator=$USER` (NixOS: `services.tailscale.extraSetFlags = [ "--operator=you" ]`). bnm tells you this in the VPN list until it is done.

### Debian and Ubuntu note

Those distros ship without unprivileged ping sockets. The deb installs `/usr/lib/sysctl.d/50-bnm.conf` to enable them; until a reboot or `sysctl --system`, probes fall back to TCP connect timing.

## How it is built

Go for the daemon, CLI and TUI; Rust and TypeScript for the desktop app. `internal/core` is the domain model (see `CONTEXT.md` for the vocabulary); `internal/nm` talks D-Bus to NetworkManager directly; `internal/vpn/*` are the backend adapters; `internal/monitor` is the probe engine; `internal/daemon` + `internal/api` expose HTTP/JSON over a Unix socket that `internal/client` wraps for the CLI, TUI and desktop app. `docs/API.md` lists the routes. `desktop/` holds the Tauri app: `desktop/src-tauri` is the Rust shell (socket, tray, config file), `desktop/src` the React frontend, `desktop/CONTRACT.md` the boundary between them. Design decisions and the research behind them live in this repo's issues (the wayfinder map, #1) and `docs/research/`.

Tests: `make test-race` (unit, no network), `make test-integration` (python-dbusmock NetworkManager), and `go test -tags live ./...` for read-only checks against the machine you are on.

## Licence

MIT.
