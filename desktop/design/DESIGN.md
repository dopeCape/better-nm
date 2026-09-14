# bnm desktop: design system and mockups

Static HTML and CSS the Tauri + React build lifts directly. Open any `0x-*.html` in a
browser; the review control at the bottom left flips presets (`[` and `]` cycle them),
density and font. Query params: `?theme=nord`, `?density=compact`, `?font=inter`,
`?state=...` (per-page states listed below), `?chrome=0` hides the review control.

## Design Read

Reading this as: a dark-first Linux desktop utility (a NetworkManager front end) for
keyboard-driven Hyprland / Waybar / Catppuccin users, with a Linear / Raycast-style
minimalist language, leaning toward native CSS tokens + Geist and JetBrains Mono +
Phosphor icons, hairlines instead of cards, and exactly one accent.

The previous Fyne app failed on two axes the toolkit could not fix: no type scale
(everything 14 px, headings just bold), and no materials (boxes, checkboxes, a chart
floating with no axis or baseline). This design fixes those with typography, spacing
and a real chart, not with decoration.

## Dials

| Dial | Value | Why |
|---|---|---|
| `DESIGN_VARIANCE` | 4 | Product UI: a fixed rail and one content column. Asymmetry is limited to the 3:2 overview split and left-aligned everything. Predictable beats artful here. |
| `MOTION_INTENSITY` | 3 | Only motivated motion: 120 ms state changes, the sparkline drawing in (storytelling: "history"), switches settling (feedback), rows fading in on discovery (state), the speed counter (feedback). All collapse under `prefers-reduced-motion`. |
| `VISUAL_DENSITY` | 6 | A daily app for people who read IPs: 13 px body, 36 px rows, mono for every machine value, hairlines not boxes. `compact` density drops to 12 px / 30 px for a 1120 x 720 window. |

## Principles

1. One accent. Everything else is grey, and the two identity colours (`--wifi`, `--vpn`) appear only on the thing they name.
2. Hairlines separate; boxes are for inputs, dialogs and the connection pill only.
3. Machine values are mono: IPs, RTTs, Mbit/s, channels, times, UUIDs, hostnames.
4. Labels are sentence case. No uppercase eyebrows, no section numbers.
5. Every colour is a token; a preset is a flat map of 16 values. No colour literal outside `presets/`.
6. 4 px radius everywhere, including switches. Nothing is a pill, nothing is a circle except status dots.
7. Keyboard first: every action shows its key on hover or always (`kbd`), the palette is `Ctrl K`.
8. State is text plus an icon, never colour alone (badges say "Connected", banners have a glyph).
9. Empty and error states are composed, not blank: icon, one line of why, one action.
10. Motion has a reason or does not happen. 120 ms for hover and colour, 200 ms for switches, dialogs and rows, 600 ms for the sparkline draw.

## Files

```
desktop/design/
  tokens.css            fonts, type scale, spacing, radius, layout, motion, density
  base.css              reset, typography, shell, every component
  presets/*.css         dark (default), light, catppuccin-mocha, catppuccin-latte,
                        gruvbox-dark, nord, tokyo-night, rose-pine, dracula, one-dark
  theme-switcher.js     review-only preset / density / font control
  icons/                Phosphor SVGs (regular, fill, duotone), fetch.sh, build-sprite.py, sprite.js
  fonts/                Geist, Inter, JetBrains Mono (variable woff2, OFL)
  01-overview.html ... 08-command-palette.html
  shots/                render.mjs and the PNGs it produces
```

Page states (`?state=`): `02-wifi` `empty`, `error`; `03-vpn` `needs-setup`; `04-quality`
`ok` (default), `learning`, `degraded`; `05-speed` `idle` (default), `running`, `result`;
`07-settings` `palette` (opens the custom palette editor).

Every value on the pages is mock data shaped like the `/v1` responses in `docs/API.md`,
seeded from the dev box (ALHN-F832-5 at 74 % on 5 GHz channel 161, gateway
192.168.1.254, Tailscale with two peers, an OpenVPN profile "tejas", a 21 Mbit/s speed
test). Neighbouring SSIDs, peers and the history rows are invented. The build shows what
the daemon returns and does not carry these strings over.

## Tokens

Sixteen colours. Semantics, then the Base16 slot a scheme converter fills them from.

| Token | Used for | Base16 | Note |
|---|---|---|---|
| `--base` | window and content background | `base00` | |
| `--mantle` | rail, top strip | derived | `base00` shifted 3 % toward black (both light and dark) |
| `--surface` | inputs, hover rows, kbd, chips, badges | `base01` | |
| `--overlay` | dialogs, palette, popovers, toasts | derived | dark: midpoint of `base00` and `base01`; light: `base00` lifted toward white |
| `--text` | primary text | `base05` | |
| `--subtext` | secondary text, labels, hints | `base04` | |
| `--muted` | placeholders, disabled, axis ticks | `base03` | |
| `--accent` | focus ring, active nav, primary button, switch on, live line, wordmark | `base0D` | the only accent |
| `--accent-text` | text on an accent background | `base00` | dark schemes; light schemes use `base07` |
| `--ok` | connected, within baseline | `base0B` | |
| `--warn` | learning, needs setup, needs auth | `base0A` | |
| `--error` | wrong password, degraded, lost probe | `base08` | |
| `--vpn` | VPN identity (shield when up, exit node) | `base09` | |
| `--wifi` | Wi-Fi identity (signal bars, wifi glyph) | `base0C` | |
| `--border` | hairlines | `base02` | |
| `--selection` | selected row, active nav background | derived | `--accent` at 15 % alpha (`#rrggbb26`) |

`base06`, `base07` (light foregrounds), `base0E` (magenta) and `base0F` (brown) are not
used; a converter may ignore them. Dracula's `base0D` is purple; the shipped Dracula
preset deliberately uses its cyan for `--accent` and keeps everything else mechanical.

Non-colour tokens live in `tokens.css`: `--font-ui`, `--font-mono`; type scale
`--fs-xs 11` `--fs-sm 12` `--fs-md 13` `--fs-lg 14` `--fs-xl 16` `--fs-2xl 20`
`--fs-3xl 28` `--fs-display 56`; spacing `--s-1 4` through `--s-16 64` on a 4 px grid;
`--r 4px`; layout `--rail-w 212` `--top-h 44` `--gutter 24` `--content-max 1040`
`--row-h 36` `--control-h 28`; motion `--t-fast 120ms` `--t-med 200ms` `--t-draw 600ms`
with `--ease cubic-bezier(.2,0,0,1)`.

Density: `<html data-density="compact|comfortable">`. Font: `<html data-font="inter|system">`.

A YAML theme is therefore:

```yaml
theme:
  preset: catppuccin-mocha        # or "custom"
  custom:                          # only when preset = custom; 16 keys
    base: "#1e1e2e"
    mantle: "#181825"
    # ...
  density: default                 # compact | default | comfortable
  font: geist                      # geist | inter | system
```

## Type

- UI: Geist variable (bundled), Inter as the alternative, system as the fallback. 13 px body at 430 weight (variable fonts read thin on dark; 430 compensates), 520 for medium, 600 for headings.
- Mono: JetBrains Mono variable (bundled), falling back to the user's Nerd Font (`JetBrainsMono Nerd Font`, `Cascadia Mono`). `tabular-nums` always, so columns of RTTs align.
- Headings: page title 20 px, section title 14 px semibold. No display sizes except the speed counter (56 px mono) and stat figures (28 px mono).

## Layout

- Rail 212 px on `--mantle`: wordmark, seven nav items (icon + label + key hint), spacer, connection pill.
- Top strip 44 px: Wi-Fi glyph, connection name, IP (mono), connectivity dot + "Internet", quality badge, search affordance with `Ctrl K`.
- Content: 24 px gutters, `max-width 1040`, sections separated by a hairline and 24 px, never boxed.
- Two-column pages use `3fr 2fr` (overview) or `1fr 1fr` (VPN); the Wi-Fi page is list + 320 px detail pane.
- Everything must fit 1120 x 720 without horizontal scroll; vertical scroll is allowed on Quality, Speed and Settings.

## Components (base.css)

| Class | What | Notes |
|---|---|---|
| `.btn` `.btn-primary` `.btn-ghost` `.btn-danger` `.btn-icon` `.btn-lg` | buttons | 28 px, accent fill only for the one primary action per view; `:active` moves 0.5 px |
| `.switch[aria-checked]` | switch | 30 x 16, 4 px radius track, square knob, settles with a small overshoot |
| `.input` `.select` `.field` | text input, select, label-above field with `.help` and `.error` | `aria-invalid` turns the border to `--error` |
| `.checkbox` `.segmented` | checkbox, segmented control | |
| `.row` (`.two` for two-line) `.list-head` | list row | grid, hover `--surface`, selected `--selection` with a 2 px inset accent bar; `.enter` fades in |
| `.sig[data-level=0..4]` | four-bar signal glyph | lit bars use `--wifi` |
| `.badge` `.ok .warn .error .accent .vpn` | state badge | text plus icon, tinted background |
| `.tag` | kind tag (WPA2, OpenVPN, exit node) | outlined, 11 px |
| `.table` | table | mono right-aligned `.num` columns, bottom hairlines only |
| `.kv` (`.tight` `.narrow`) | key / value grid | 112 px keys (88 in the detail pane) |
| `.dialog` + `.scrim` | modal | 440 px, `--overlay`, hairline, no shadow; scrim is `--mantle` at 70 % |
| `.toast` `.toast-stack` | transient notice | bottom right |
| `kbd` `.keys` | key hint | 18 px, mono 11 px |
| `.tooltip` | tooltip | `--overlay`, hairline |
| `.banner` `.ok .warn .error .accent` | state strip | icon, title, one line, optional action |
| `.state` (`.error`) | empty and error states | dashed hairline box, icon, title, one line, action |
| `.progress` | segmented phase bar | 4 px, one segment per phase |
| `.chart` | SVG line chart | `.band` baseline band behind `.line`; loss ticks; `.axis` mono ticks; `.draw` animates in |
| `.stat` `.stats` | figure with unit and label | mono figure, unit in `small` |
| `.palette` | command palette | 560 px, groups, `aria-selected` row |
| `.conn-pill` | rail connection pill | dot, name, IP, VPN shield when up |

## Icons (Phosphor)

Regular weight for navigation and actions, Fill for state, Duotone reserved for the tray
and empty states. Inline via `icons/sprite.js` as `<svg class="ic"><use href="#ph-name"/></svg>`.

Navigation: `house` `wifi-high` `shield-check` `pulse` `gauge` `network` `gear-six`.
Top strip and rail: `broadcast` (wordmark) `magnifying-glass` `wifi-high`.
Wi-Fi: `arrows-clockwise` `lock-simple` `check` `eye` `eye-slash` `trash` `wifi-slash` `cell-signal-none` `funnel`.
VPN: `shield-check` + `shield-check-fill` `shield-warning` + `shield-warning-fill` `file-arrow-up` `plus` `copy` `sign-in` `sign-out` `arrow-square-out` `laptop` `device-mobile` `desktop` `caret-up-down`.
Quality: `pause` `play` `arrow-counter-clockwise` `check-circle-fill` `warning-circle-fill` `spinner-gap`.
Speed: `gauge` `lightning` `download-simple` `upload-simple` `copy`.
Dialogs and palette: `key` `x` `clock-countdown` `arrow-elbow-down-left` (Enter glyph) `arrow-right` `globe`.
Settings: `palette` `text-aa` `rows` `bell` `terminal-window` `list-dashes` `caret-right`.
Events: `wifi-high` `shield-check` `check-circle` `warning-circle` `pulse`.

The full fetched set (104 symbols) is in `icons/fetch.sh`; anything not listed there is
not part of the design. Never Lucide, never Heroicons, never emoji, never hand-drawn paths.

## Interaction

Keyboard map:

| Key | Action |
|---|---|
| `1` .. `7` | Overview, Wi-Fi, VPN, Quality, Speed, Devices, Settings |
| `Ctrl K` | command palette (search networks, run actions, go to) |
| `/` | focus the filter on list pages |
| `R` | rescan Wi-Fi |
| `A` | add VPN from file |
| `P` | pause / resume monitoring |
| `Enter` `Q` | run / quick speed test on the Speed page; `Esc` cancels |
| `Enter` | connect / apply in the focused form or dialog; `Esc` cancels |
| `↑` `↓` `Enter` | move and act in lists and the palette |
| `G` then `O/W/V/Q/S/D/T` | go-to chords (also listed in the palette) |

The palette label reads `Ctrl K` because bnm is a Linux app; a macOS build shows `⌘K`.

Focus: `:focus-visible` draws `0 0 0 2px --base, 0 0 0 4px --accent`, never `outline`.
Inputs on focus: accent border plus a 3 px `--selection` halo. Focus is never hidden.

Motion (all under `prefers-reduced-motion: no-preference`; the reduce block sets the
durations to 0 and removes the draw):

| Thing | Duration | Reason |
|---|---|---|
| hover, colour, border | 120 ms | acknowledge the pointer |
| switch knob | 200 ms with a slight overshoot | feedback that the write happened |
| dialog, palette, toast enter | 200 ms fade + 4 px rise | show where it came from |
| list row enter | 200 ms fade + 2 px rise, 24 ms stagger | rows arrive as the scan finds them |
| sparkline draw | 600 ms stroke-dash | history draws left to right, once, on mount |
| speed counter | eased count to the live sample, 8 updates/s | the number is the test |

Nothing loops. The `spinner-gap` icon in the learning badge is static in the mockups; the
build may rotate it at 1 turn/s, and must stop it under reduced motion.

## What the build agents must not do

- No colour literal outside `presets/*.css`. Every colour is `var(--token)` or a `color-mix()` of tokens.
- No radius other than `var(--r)` (4 px) and the 3 px used inside switches, checkboxes and kbd. No pills, no circles except `.dot`.
- No shadows. Dialogs and popovers separate with the scrim and a hairline. No gradients, no blur, no glass.
- No cards. Group with hairlines and space. Boxes are only inputs, dialogs, the connection pill, the empty state and swatches.
- No uppercase eyebrow labels, no section numbers, no "·" chains (one middle dot per line at most).
- No emoji, no Lucide, no Heroicons, no hand-drawn SVG paths. Add a Phosphor icon by name in `icons/fetch.sh`.
- No proportional digits in tables or figures; keep `tabular-nums` and the mono face for machine values.
- No text lighter than `--muted` on `--base`. No text on an accent background other than `--accent-text`.
- No three-equal-cards rows, no centered hero blocks except the Speed hero, which is one figure.
- No motion beyond the table above. No infinite spinners except the learning badge, and none under reduced motion.
- No dual-axis charts, dashed grids or filled comparison bars. One series per chart, the baseline band behind it.
- Do not put a page title where the top strip already says it; the `h1` is the section name, 20 px, once.
- Do not invent copy. The strings in the mockups are the strings; change them only to match a real API value.

## Rendering the screenshots

```
node desktop/design/shots/render.mjs                       # dark, 1440x900 and 1120x720, every page and state
node desktop/design/shots/render.mjs --theme nord --quick  # one preset, 1440x900 only
node desktop/design/shots/render.mjs --pages "01-overview.html 02-wifi.html"
```

Drives `google-chrome` or `chromium` over the DevTools protocol (Node 22+, no npm
deps; `nix shell nixpkgs#chromium --command node desktop/design/shots/render.mjs`).
Chrome's plain `--screenshot` flag was tried first and races both the webfont load and
its own viewport resize, so half the stills came out 88 px short or in fallback fonts;
CDP waits for `document.fonts` and sets the viewport explicitly.
