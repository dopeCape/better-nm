# bnm desktop: shell ⇄ frontend contract

The desktop app is a Tauri 2 shell (Rust, `desktop/src-tauri`) hosting a React app (`desktop/src`).
The shell owns everything that touches the OS: the Unix socket to `bnmd`, the YAML config file,
the tray, file pickers, URL opening. The frontend owns everything visible. They talk only through
the commands and events below. Both sides implement this file; neither invents extra surface
without updating it.

## Daemon access

The frontend never opens the socket. Every `bnmd` call goes through one command:

```ts
invoke<ApiResponse>("api_request", { method: "GET" | "POST" | "PUT" | "DELETE", path: "/v1/status", body?: unknown })
// -> { status: number, body: string }   // body is the raw JSON text from bnmd ("" for 204)
```
- `path` is the full API path from `docs/API.md` including `/v1`, query string allowed.
- Non-2xx is NOT an error at the invoke level: the frontend parses `{error, hint, code}` from `body`.
- Transport failures (socket missing, daemon died) reject the promise with the string `"daemon-unreachable: <detail>"`. A `method` outside GET/POST/PUT/DELETE or a `path` not under `/v1` rejects with a plain message (a frontend bug, not a daemon state).
- The shell auto-starts `bnmd` on first use if the socket is absent (same rules as the Go client: `$XDG_RUNTIME_DIR/bnm/bnmd.sock`, fallback `/tmp/bnm-<uid>/bnmd.sock`; binary next to the app executable, then `bnmd` on PATH; logs to `$XDG_STATE_HOME/bnm/bnmd.log`), waits up to 3 s.
- Version check: the shell reads `api_version` from `/v1/status` once and rejects with `"api-mismatch: daemon speaks v<n>, app needs v1"` if it differs.

### Live stream
`invoke("stream_start")` starts (idempotently) a reader of `GET /v1/events/stream`. The shell then emits Tauri events, payload = the JSON object from the stream:

| event name | payload |
|---|---|
| `bnm://change` | `core.Change` `{kind, path?}` |
| `bnm://event` | `core.Event` (types incl. `secret-needed`, `secret-resolved`) |
| `bnm://stream` | `{connected: boolean, error?: string, attempt?: number}` on every connect/disconnect/retry |

The shell reconnects with backoff 0.5 s → 10 s and keeps running until the app quits. `invoke("stream_stop")` exists for tests.
The shell starts the reader itself at launch (the tray depends on it), so `stream_start` from the frontend is a no-op in practice; every reconnect attempt goes through the auto-start path, so a daemon that dies is respawned by the app. 45 s without any frame (the daemon pings every 15 s) counts as a disconnect.

### Speed test
`invoke("speed_start", { opts })` where `opts` is `core.SpeedOptions`; the shell POSTs `/v1/speed` (SSE) and emits `bnm://speed` with payload `{phase: "progress", data: core.SpeedProgress} | {phase: "result", data: core.SpeedResult} | {phase: "error", error: string}`. `invoke("speed_cancel")` aborts it and emits `{phase: "error", error: "cancelled"}`. One test at a time; a second start while running rejects with `"speed-running"`. A non-2xx answer to the POST (409 while the daemon runs another test) arrives as `{phase: "error", error: <error text from the body>}`.

## Desktop config (YAML)

File: `$XDG_CONFIG_HOME/bnmdesktop/config.yaml` (default `~/.config/bnmdesktop/config.yaml`). Created with defaults and a commented header on first run if absent. Watched with inotify; the shell debounces 150 ms and emits `bnm://config` with the parsed config on every change (also once right after `config_get`).

```yaml
# bnm desktop configuration. Every key is optional; this file shows the defaults.
theme: dark            # preset id: dark | light | catppuccin-mocha | catppuccin-latte | gruvbox-dark
                       #            | nord | tokyo-night | rose-pine | dracula | one-dark | custom
accent: ""             # optional hex override of the preset's accent, e.g. "#89b4fa"
font: ""               # UI font family; "" = bundled Geist
mono_font: ""          # machine-value font; "" = bundled JetBrains Mono (a Nerd Font name works)
density: default       # compact | default | comfortable
sidebar: left          # left | hidden
start_section: overview
tray: auto             # auto | on | off
close_to_tray: true
reduced_motion: false  # true forces the no-motion variant regardless of the OS setting

custom:                # used when theme: custom; any token omitted falls back to `dark`
  base: "#141517"
  mantle: "#0f1012"
  surface: "#1c1e21"
  overlay: "#1f2124"
  text: "#e7e9ec"
  subtext: "#a0a5ad"
  muted: "#737880"
  accent: "#5fa8c8"
  accent_text: "#0b1215"
  ok: "#52b788"
  warn: "#e3b341"
  error: "#e5695f"
  vpn: "#e08a4f"
  wifi: "#6db7d6"
  border: "#26292d"
  selection: "#5fa8c826"

base16: ""             # path to a Base16 YAML scheme; when set and theme: custom, tokens derive
                       # from it using the mapping in desktop/design/DESIGN.md; `custom` keys
                       # then override individual tokens
import: ""             # path to a pywal/wallust colors.json; same precedence as base16
```

Commands:
- `invoke<DesktopConfig>("config_get")` returns the effective config (defaults merged, paths expanded, `base16`/`import` files already resolved into `custom` tokens, plus `{source_path: string, errors: string[]}` for unparsable keys).
- `invoke("config_set", { patch })` merges a partial config into the file and rewrites it (comments in the header preserved; user comments elsewhere may be lost, say so in the docs). The watcher then emits `bnm://config`. A `config.yaml` that is a symlink (stow, chezmoi, home-manager) is written through: the link stays a link and the target is replaced atomically; a read-only target rejects with the OS error.
- `invoke<string>("config_path")`.

Resolution rules the shell applies (so the frontend can use `custom` as the effective palette for every theme):
- `custom` in the effective config always carries all sixteen tokens: the chosen preset's values, or for `theme: custom` the `dark` preset overlaid by the Base16 file, then the pywal file, then the file's own `custom` keys, in that order. `base16`, `import` and the file's `custom` keys have no effect unless `theme: custom`.
- `accent` (any theme) replaces `custom.accent` and re-derives `custom.selection` (accent at 15 % alpha); a `custom.accent` without a `custom.selection` does the same.
- Relative `base16`/`import` paths resolve against the config directory; `~/` expands. An unreadable or malformed file adds an entry to `errors` and leaves the previous layer in place.
- Unknown keys, values outside the allowed sets, and non-`#rrggbb[aa]` colours each add one entry to `errors`; the key keeps its default. `config_set` rejects (and does not write) a patch that would introduce a new error; `null` in a patch removes the key. The shell emits `bnm://config` itself right after a successful `config_set`, and the watcher emits again when inotify reports the rewrite: expect two identical events.
- `tray` changes apply live: the tray is built or removed on the next `bnm://config`. Removing the tray while the window is hidden shows the window again.

The TypeScript type `DesktopConfig` in `desktop/src/config/types.ts` is the source of truth for the frontend; the Rust `struct DesktopConfig` in `desktop/src-tauri/src/config.rs` must serialise to the same JSON (snake_case keys as above).

## Other shell services

- `invoke<{name: string, content: string} | null>("pick_vpn_file")`: native file dialog filtered to `.conf`/`.ovpn`, returns the file content (a regular file, ≤ 1 MiB, UTF-8; anything else rejects with a plain message) or null on cancel.
- `invoke("open_url", { url })`: opens an `http(s)://` URL in the default browser (Tailscale login); anything else rejects.
- `invoke<DaemonStatus>("daemon_status")` → `{running: boolean, socket: string, pid?: number, version?: string, unit_installed: boolean, unit_active: boolean}`.
- `invoke("daemon_install")`: same steps as `bnm daemon install` (writes the user unit, `systemctl --user daemon-reload`, `enable --now`); rejects with the systemctl error text. The unit's `ExecStart` is the `bnmd` the shell would auto-start: `BNM_DAEMON`, then next to the executable, then `PATH`. From an AppImage "next to the executable" means next to the `.AppImage` file and the mounted squashfs is never used (neither for the unit nor for auto-start): it is gone when the app exits.
- `invoke("daemon_restart")`: `systemctl --user stop`/`start` when the unit is active on the default socket, else SIGTERM to the pid in `bnmd.pid` beside the socket and a fresh detached spawn; waits for the socket and re-runs the version check. The stream reconnects by itself (`bnm://stream` false, then true).
- `invoke("window_show")`: shows and focuses the main window (secret prompts call this).
- `invoke("window_hide")`: hides the window to the tray (the palette's "Hide to tray"). Rejects with `"no-tray"` when no tray icon exists, since nothing could bring the window back; the frontend offers the action only when `tray_present` is true.
- `invoke("window_close")`: quits the app (exit code 0). The window is frameless, so the palette's "Quit bnm" is the close button. It ignores `close_to_tray` on purpose; hiding is `window_hide`.
- `invoke<boolean>("tray_present")`: whether a tray icon exists right now (`tray: auto` depends on the desktop; `tray` changes apply live, so re-ask after every `bnm://config`).
- `invoke("config_reveal")`: opens the directory holding the desktop config file in the file manager (`open_url` is http(s) only).
- `invoke("notify", { title, body? })`: only for the frontend's own toasts if ever needed; daemon notifications stay in the daemon.

## Tray

Owned by the shell; the frontend only asks `tray_present` and calls `window_hide`. Built when a StatusNotifier host exists and `tray != off` (or always when `tray: on`). Menu: connection line (disabled item), `Wi-Fi on/off` (check item), one check item per VPN (from `/v1/vpn`), `Quality: <verdict>` (disabled), separator, `Open bnm`, `Quit`. The shell refreshes it from `bnm://change` hints of kind `status`, `wifi`, `vpn`, `monitor`, and performs actions by calling the API itself. Closing the window hides it when a tray exists and `close_to_tray` is true; otherwise quits.

## Window

Title `bnm`, default 1120×720, min 900×600, remembers size/position via `tauri-plugin-window-state`. Single instance (`tauri-plugin-single-instance`: a second launch focuses the first). `--section <name>` CLI flag and `bnm-desktop://section/<name>` are not required for v1.

## Frontend routing of events

The frontend subscribes once at startup: `bnm://change` → invalidate the affected query (status, devices, wifi, profiles, active, vpn, monitor); `bnm://event` → append to the events feed, and for `secret-needed` fetch `/v1/secrets/{id}` and open the prompt (calling `window_show` first); `bnm://config` → apply theme/density/font; `bnm://stream` → show/hide the "daemon unreachable, retrying" banner.
