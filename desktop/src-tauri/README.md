# bnm desktop shell (Tauri 2)

The Rust side of the desktop app. It implements the shell half of
[`desktop/CONTRACT.md`](../CONTRACT.md): the Unix socket to `bnmd`, the event and
speed streams, the YAML config file, the tray, the file picker, URL opening. The
React frontend in `desktop/src` implements the other half and never touches the OS.

## Layout

```
src-tauri/
  Cargo.toml                 crate bnm-desktop, lib bnm_desktop_lib (so tests can link it)
  tauri.conf.json            window, CSP, bundle targets, frontendDist
  capabilities/default.json  what the webview may call: core:default only
  bnm-desktop.desktop.hbs    the .desktop template deb/rpm install
  icons/                     generated from packaging/desktop/bnm.png (cargo tauri icon)
  devdist/index.html         placeholder frontend so the shell builds without desktop/src
  src/
    main.rs        calls bnm_desktop_lib::run
    lib.rs         plugin setup, AppState, window close -> tray, tracing
    daemon.rs      socket discovery, auto-start, hyper client, version check, status/install/restart
    stream.rs      SSE parser, /v1/events/stream reader with backoff, speed-test runner
    config.rs      DesktopConfig, tolerant YAML loader, palette resolver, patch writer, watcher
    tray.rs        StatusNotifier detection, menu model, tray actions
    commands.rs    every invoke command of the contract
  tests/daemon_live.rs   integration test against a real `bnmd --fake`
```

## Build, run, test

Everything runs inside the flake's dev shell (rustc, cargo, cargo-tauri, node, pnpm,
webkitgtk 4.1, gtk3, libayatana-appindicator):

```sh
nix develop                                    # from the repo root
cd desktop/src-tauri
cargo build                                    # debug binary: target/debug/bnm-desktop
cargo tauri build --no-bundle                  # release binary: target/release/bnm-desktop
cargo tauri build                              # + AppImage, deb, rpm under target/release/bundle/
cargo fmt && cargo clippy --all-targets -- -D warnings
cargo test                                     # unit tests + tests/daemon_live.rs (needs `go`)
BNM_DESKTOP_LOG=debug ./target/debug/bnm-desktop
```

`cargo tauri dev` starts the app against `build.devUrl` (`http://localhost:1420`,
the Vite dev server). `build.beforeDevCommand` is empty here; the orchestrator sets it
to `pnpm dev` once `desktop/src` exists.

Logging goes to stderr; `BNM_DESKTOP_LOG` takes a `tracing` filter (`info` by default,
`debug`, `bnm_desktop_lib::stream=trace`, ...).

### Switching from the placeholder to the real frontend

`tauri.conf.json` has `build.frontendDist = "devdist"`, a one-file placeholder, so the
shell compiles and runs without the React app. When `desktop/src` builds:

1. set `"frontendDist": "../dist"` (Vite's output, relative to `src-tauri`),
2. set `"beforeBuildCommand": "pnpm build"` and `"beforeDevCommand": "pnpm dev"`,
3. delete `devdist/`.

Nothing in the Rust code depends on the placeholder.

## The integration test

`tests/daemon_live.rs` runs `go build ./cmd/bnmd` into a temp dir, starts it with
`--fake` on a temp socket through the client's own auto-start path, and checks:
`GET /v1/status` (api_version 1), `GET /v1/wifi`, a POST with a body, a 404 whose
`{error, code}` body passes through, a 400 for unknown JSON fields, `daemon_status`,
the stream reader connecting and delivering a change hint, `daemon_restart`
(SIGTERM + respawn + stream reconnect), and `stream_stop`. It skips itself when `go`
is not on PATH. `XDG_STATE_HOME` / `XDG_CONFIG_HOME` are pointed at the temp dir so
the fake never touches your real state.

## How commands map to the contract

| `invoke` | Rust | Notes |
|---|---|---|
| `api_request {method, path, body?}` | `commands::api_request` → `daemon::Client::request` | one hyper HTTP/1.1 connection per request over `UnixStream`; non-2xx returned as `{status, body}`; transport failure rejects `daemon-unreachable: ...`, wrong `api_version` rejects `api-mismatch: daemon speaks v<n>, app needs v1`. Auto-starts `bnmd` (BNM_DAEMON, then next to the executable, then PATH) with `setsid`, cwd `/`, stdio appended to `$XDG_STATE_HOME/bnm/bnmd.log`, waits ≤ 3 s. |
| `stream_start` / `stream_stop` | `stream::StreamRunner` | reads `GET /v1/events/stream`, emits `bnm://change`, `bnm://event`, `bnm://stream`; backoff 0.5 s → 10 s; 45 s of silence (the daemon pings every 15 s) counts as a dead connection. The shell already starts it at launch (the tray needs it); `stream_start` is idempotent. |
| `speed_start {opts}` / `speed_cancel` | `stream::SpeedRunner` | `POST /v1/speed` as SSE, emits `bnm://speed` `{phase: progress\|result\|error}`; a second start rejects `speed-running`; cancel aborts the connection (which cancels the daemon-side test) and emits `{phase: "error", error: "cancelled"}`. |
| `config_get` / `config_set {patch}` / `config_path` | `config::ConfigStore` | see below |
| `pick_vpn_file` | `tauri-plugin-dialog` | filter `.conf`/`.ovpn`; regular files only (a FIFO would block the command), ≤ 1 MiB read through a bounded reader, UTF-8; `null` on cancel |
| `open_url {url}` | `tauri-plugin-opener` | http(s) only |
| `daemon_status` | `daemon::Client::status` | `{running, socket, pid?, version?, unit_installed, unit_active}` without auto-start |
| `daemon_install` | `daemon::Client::install` | writes `~/.config/systemd/user/bnmd.service` with the absolute `bnmd` path (quoted when it contains spaces), stops a hand-started daemon, `systemctl --user daemon-reload`, `enable --now`, waits for the socket; rejects with systemctl's text. Refuses a path inside an AppImage mount |
| `daemon_restart` | `daemon::Client::restart` | `systemctl --user stop/start` when the unit is active on the default socket, else SIGTERM the pid from `bnmd.pid` and respawn; waits for the socket; the stream reconnects by itself |
| `window_show` | `show_main_window` | show + unminimize + focus |
| `window_hide` | `hide_main_window` | hide; rejects `no-tray` without a tray icon |
| `window_close` | `AppHandle::exit(0)` | quit (the frameless window's close button lives in the palette) |
| `tray_present` | `tray::present` | whether a tray icon exists right now |
| `config_reveal` | `tauri-plugin-opener` `open_path` | opens the config directory in the file manager |
| `notify {title, body?}` | `tauri-plugin-notification` | |

Start/stop/restart/install and the auto-start path share one lock, so the stream
reader (which also auto-starts on each reconnect) never races a restart into two daemons.

### Running from an AppImage

The AppImage runtime sets `APPIMAGE` (the file) and `APPDIR` (the squashfs mount under
`/tmp/.mount_*`). `daemon::Whereabouts::find_daemon` then treats "next to the
executable" as next to the `.AppImage` file, skips `PATH` entries under the mount, and
never returns a binary from inside it: a bnmd started from there would lose its pages
when the mount goes away with the app, and a unit pointing there would fail at the next
boot. `daemon_install` also refuses a `BNM_DAEMON` inside the mount. Nothing bundles
`bnmd` into the AppImage today (no `externalBin`); users get it from the `bnm` package.

### Config

File: `$XDG_CONFIG_HOME/bnmdesktop/config.yaml`. Written with the contract's commented
defaults on first run. Loading never fails: unknown keys, bad enum values, non-hex
colours and unreadable `base16`/`import` files each become one entry in `errors`, and
the key keeps its default.

`resolve()` fills `custom` with the *effective* palette for every theme:

1. the preset's sixteen tokens (`dark` when `theme: custom`),
2. when `theme: custom`: the Base16 scheme file (`palette:` map or legacy top-level
   `base00..base0F`; `variant:` or the luminance of `base00` decides dark/light; mapped
   per DESIGN.md), then the pywal `colors.json` (background/foreground/color0..15,
   surfaces mixed from background and foreground), then the file's explicit `custom`
   keys (a custom `accent` re-derives `selection` unless `selection` is given too),
3. the top-level `accent` (any theme), which also re-derives `selection`.

Relative `base16`/`import` paths resolve against the config directory; `~/` expands.

`config_set` merges the JSON patch into the YAML document (`custom` key by key, `null`
removes a key), validates it by re-parsing, and writes atomically. The leading comment
block is preserved; comments beside keys are lost on the first write. The patch is
rejected (no write) when it introduces a new error.

The watcher (`notify`, inotify) watches the *directory*, filters to the file name and
ignores access events, debounces 150 ms, then reloads and emits `bnm://config`. When
`config.yaml` is a symlink the target's directory is watched too, and writes go through
the link (temp file beside the target, rename over it), so a dotfile-managed config keeps
working. The `tray:` setting is re-applied on every change (a tray is built or removed
live; removing it while the window is hidden shows the window).

### Tray

`tray: auto` builds the tray only when `org.kde.StatusNotifierWatcher` (or the
`org.freedesktop.` spelling) has an owner on the session bus, checked with `dbus-send
... NameHasOwner`, falling back to `busctl --user status`; with neither tool there is no
tray. `on` forces it, `off` disables it. Menu: connection line, `Wi-Fi on/off` check
item, one check item per VPN (disabled unless `writable` and in a state bnm can flip),
`Quality: <verdict>`, separator, `Open bnm`, `Quit`. It refreshes from `status`, `wifi`,
`vpn`, `monitor` change hints and on stream connect/disconnect, coalescing bursts.
`set_menu` blocks until the GTK main thread has run it, and that thread also takes the
tray mutex (window close, menu events), so `refresh` clones the icon handle out of the
lock before calling it. Menu events go through one `App::on_menu_event` listener
registered at setup, not one per built tray, and removal goes through
`remove_tray_by_id` (the app's registry keeps its own handle; dropping ours alone would
leave the icon on screen).
The icon is a monochrome (light, alpha-preserving) variant of the app icon computed
at startup from `icons/128x128.png`. Closing the window hides it when a tray exists and
`close_to_tray` is true; otherwise the app quits.

### Security

`capabilities/default.json` grants the main window `core:default` only. Every plugin
(dialog, opener, notification, window-state, single-instance) is driven from Rust behind
the commands above, so nothing plugin-shaped is reachable from the webview. The CSP is
strict except `style-src 'unsafe-inline'` (the theme engine writes inline styles);
`dangerousDisableAssetCspModification: ["style-src"]` keeps Tauri from adding style
hashes, which would silently disable `'unsafe-inline'`.

### Bundling

`bundle.targets = [appimage, deb, rpm]`. deb depends on `network-manager` and recommends
`libnotify4`; rpm depends on `NetworkManager` and recommends `libnotify`. The desktop
entry template is `bnm-desktop.desktop.hbs` (installed as `bnm-desktop.desktop` because
`mainBinaryName` is `bnm-desktop`; categories `Network;Settings;`). The package name
follows `productName` (`bnm-desktop`), so it never collides with the nfpm `bnm` package
that ships the CLI and daemon: `bnm-desktop_<ver>_<arch>.deb`, `bnm-desktop-<ver>-1.<arch>.rpm`,
`bnm-desktop_<ver>_<arch>.AppImage`. The version lives in `Cargo.toml` only (`tauri.conf.json`
has no `version` key, so Tauri falls back to it); it stays `0.1.0` in git and the release
workflow rewrites it from the tag before building. `make desktop` and `make desktop-bundle` at the
repo root wrap `pnpm tauri build`; the Nix package (`nix build .#bnm-desktop`) uses nixpkgs'
`cargo-tauri.hook`, which builds the deb and installs its `usr/` tree.
