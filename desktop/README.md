# bnm desktop: the React front end

The window contents of the Tauri app. The Rust shell in `src-tauri/` owns the socket,
the YAML config, the tray and the file dialogs; this side owns everything visible and talks
to the shell only through the commands and events in [`CONTRACT.md`](CONTRACT.md).

## Run it

Everything runs inside the repo's dev shell (`nix develop`; node 24, pnpm 11).

```
cd desktop
pnpm install
pnpm dev            # http://localhost:5173, against the real daemon, in a plain browser
pnpm test           # vitest + testing-library, mocked shell
pnpm typecheck
pnpm lint           # eslint: typescript, react-hooks, jsx-a11y
pnpm build          # -> dist/, what the Tauri shell bundles
```

### Browser mode

`pnpm dev` needs no Tauri. When there is no `window.__TAURI_INTERNALS__`, `src/shell/index.ts`
picks `src/shell/http.ts`, which implements every shell command over HTTP:

- `api_request` becomes `fetch("/api/v1/...")`. A Vite plugin in `vite.config.ts` proxies
  `/api/*` to `bnmd`'s Unix socket (`$XDG_RUNTIME_DIR/bnm/bnmd.sock`, or `BNM_SOCKET`) with
  `http.request({ socketPath })`, piping bodies both ways so the SSE event stream and the
  speed test stream flow through unbuffered.
- `stream_start` is an `EventSource` on `/api/v1/events/stream`; `speed_start` reads the
  `POST /v1/speed` SSE body with `fetch`.
- Desktop config (`config_get` / `config_set` / `bnm://config`) lives in
  `localStorage["bnmdesktop.config"]` with the same defaults as the YAML file.
- `pick_vpn_file` is an `<input type=file>`, `open_url` is `window.open`,
  `daemon_install` / `daemon_restart` reject with the CLI command to run instead.

Start the daemon first if it is not running (`bnm daemon status`). Query params:
`?section=wifi` opens a section, `?motion=0` settles every animation (screenshots).

In production (`pnpm build`) only the Tauri shell is bundled.

## Layout

```
src/
  main.tsx            boots the shell, applies the default theme, mounts <App>
  app/                the frame: Rail, TopStrip, Palette (Ctrl K), SecretPrompt, Toasts,
                      events.ts (bnm://* routing), hotkeys.ts (1..7, /, R, A, P, G chords)
  views/              Overview, Wifi (+ wifi/WifiDetail), Vpn (+ vpn/VpnRow), Quality,
                      Speed, Devices, Settings
  components/         Icon (Phosphor sprite), Chart (SVG over a baseline band), Dialog,
                      ui.tsx (Switch, Badge, Tag, Field, Select, Banner, EmptyState, Skeleton, ...)
  api/                types.ts (mirrors internal/core), client.ts (ApiError with hint),
                      queries.ts (TanStack Query + change-kind invalidation), actions.ts
  shell/              the Shell interface, tauri.ts, http.ts
  theme/              presets.ts (generated), apply.ts (config -> CSS variables and data-* attrs)
  config/types.ts     DesktopConfig, the frontend's source of truth for the YAML shape
  state/ui.ts         zustand: section, palette, toasts, stream health, secret prompt, speed run
  styles/app.css      the per-page rules from the mockups plus skeletons
  design/             lifted verbatim from ../design by scripts/sync-design.mjs (do not edit)
scripts/
  sync-design.mjs     copies base.css, tokens.css, fonts, the icon sprite and dark.css
  gen-presets.ts      parses ../design/presets/*.css into src/theme/presets.ts
  shots.mjs           screenshots the live app over CDP into shots/
```

`pnpm sync-design` re-runs both generators after a change in `desktop/design/`. The design
stays the source; nothing under `src/design/` or `src/theme/presets.ts` is edited by hand.

## Data flow

Every read is a query keyed in `api/queries.ts`; every write is a function in
`api/actions.ts` wrapped by `useAction`, which toasts the daemon's `error` and `hint` on
failure. `app/events.ts` subscribes once at startup: `bnm://change` invalidates the queries
for its `kind`, `bnm://event` feeds the events list and opens the password prompt on
`secret-needed` (after `window_show`), `bnm://stream` drives the "daemon unreachable"
banner, `bnm://config` re-applies the theme, `bnm://speed` drives the speed counter.

## Theme

`theme/apply.ts` resolves a `DesktopConfig` to the sixteen tokens: a preset, or `custom`
over `dark` for omitted keys, then the optional `accent` override (with `--selection`
derived from it). It writes them as CSS variables on `:root`, sets `data-theme`,
`data-density`, `data-motion="0"` for `reduced_motion`, `data-font` for the bundled
alternatives or `--font-ui` / `--font-mono` for a custom family, and `color-scheme`
from the preset. An unknown preset falls back to dark with a toast.

## Screenshots

```
node scripts/shots.mjs                                        # dark, 1440x900
node scripts/shots.mjs --theme catppuccin-latte
node scripts/shots.mjs --sizes 1120x720,900x600 --density compact
```

Needs `pnpm dev` running and a Chrome or Chromium on `PATH`
(`nix shell nixpkgs#chromium --command node scripts/shots.mjs`). The script only reads:
it never connects, forgets or toggles anything.
