# Go desktop toolkit for the `bnm` desktop app (Linux, Wayland/X11, tray, AppImage)

Resolves [#3](https://github.com/dopeCape/better-nm/issues/3). Researched 2026-09-07 against
primary sources only (toolkit repos and docs, compositor/bar sources, AppImage tooling).

## Summary

**Recommendation: Fyne v2.8.x** (`fyne.io/fyne/v2`), with its own `fyne.io/systray` for the
tray. It is the only candidate that, in one binary and one stable major version, picks Wayland
or X11 at runtime, ships a pure-Go StatusNotifierItem tray, links only against libraries every
desktop already has, and therefore packages as a small, boring AppImage. Runner-up is **Gio**
(same dependency profile, no tray, immediate-mode API). **Wails v3** is the pick only if the team
wants a web UI and accepts WebKitGTK as a hard runtime dependency. gotk4 and bare webview are
not recommended for this project (details below).

Two facts that bind every option and that later tickets must plan for:

1. **No Go GUI toolkit is a static binary on Linux.** All five candidates use cgo and link
   system libraries. goreleaser says "Compiling with CGO is tricky, especially when
   cross-compiling. It requires extra setup and won't work 'out of the box'"
   ([goreleaser](https://goreleaser.com/limitations/cgo/)). The daemon/CLI/TUI can stay static;
   the desktop binary needs a per-arch native build (or zig cc) with C headers installed.
2. **Tray availability is a property of the desktop, not the toolkit.** Every candidate that has
   a tray implements the KDE StatusNotifierItem (SNI) D-Bus protocol
   ([spec](https://www.freedesktop.org/wiki/Specifications/StatusNotifierItem/)). sway's bar and
   Waybar (Hyprland's recommended bar) implement the `org.kde.StatusNotifierWatcher` side
   ([sway](https://github.com/swaywm/sway/blob/master/swaybar/tray/watcher.c),
   [Waybar](https://github.com/Alexays/Waybar/blob/master/src/modules/sni/watcher.cpp),
   [Hyprland wiki](https://wiki.hypr.land/Useful-Utilities/Status-Bars/)); KDE authored it
   ([KStatusNotifierItem](https://invent.kde.org/frameworks/kstatusnotifieritem)); GNOME Shell
   needs the AppIndicator extension, which "integrates Ubuntu AppIndicators and
   KStatusNotifierItems ... into GNOME Shell"
   ([extension README](https://github.com/ubuntu/gnome-shell-extension-appindicator)).
   A bare Hyprland install with no bar has no tray at all. `bnm` must run fine with no tray host.

## Comparison

| | Wails v3 (beta) | Fyne 2.8 | Gio 0.10 | gotk4 (+adwaita) | webview_go |
|---|---|---|---|---|---|
| Wayland | via GTK4/WebKitGTK (or GTK3 with `-tags gtk3`, removed in v3.1) | Runtime pick Wayland/X11 since 2.8.0 | Wayland default, X11 fallback; `nowayland`/`nox11` tags | via GTK4 | via GTK3 |
| Tray (SNI) | Built in, pure Go (godbus) | Built in via `fyne.io/systray`, pure Go on Linux | None; add `fyne.io/systray` (`RunWithExternalLoop`) | None (GTK has no status icon); add `fyne.io/systray` | None |
| Hard runtime libs | gtk4 + webkitgtk-6.0 (+ WebKit helper processes) | libGL, X11 libs, wayland-client/cursor/egl, xkbcommon | wayland-client/cursor/egl, EGL, X11 libs, xkbcommon; GLES/Vulkan dlopen'd | gtk4, libadwaita, GLib schemas, pixbuf loaders | gtk+-3.0 + webkit2gtk-**4.0** (hard-coded) |
| AppImage | `wails3 package` bundles WebKit processes + GTK via linuxdeploy-plugin-gtk | Binary + .desktop + icon; deps are on the excludelist or trivially bundled | Same as Fyne | Must bundle whole GTK4 stack (plugin-gtk is "experimental") | Nothing provided |
| Look | Whatever you build in HTML/CSS | Own theme, same on every DE | Own Material widgets | Native GNOME/libadwaita; foreign on KDE/wlroots | Web page |
| Maintenance (2026-09) | 36.2k stars, 329 issues, beta.17 on 2026-09-06 | 28.7k stars, 733 issues, 2.8.1 on 2026-08-26 | 2.2k stars, 20 issues, v0.10.2, pushed 2026-09-02 | 694 stars, 75 issues, v0.4.1, pushed 2026-08-09 | 452 stars, last push 2024-08-31 |
| Stable major | No (v3 beta; v2 stable but no tray, GTK3/WebKit 4.0) | Yes (2.x) | No (v0.x) | No (v0.x); README warns of leaks/crashes | Yes but stale |

Stars/issues/dates are from the GitHub API on 2026-09-07.

## Per-toolkit notes

### Wails

- Two lines: "v2 | Stable", "v3 | Beta" ([README](https://github.com/wailsapp/wails#getting-started)).
  v3 cut beta.10 to beta.17 between 2026-08-19 and 2026-09-06 (GitHub releases API), so it is
  moving fast but not yet 3.0.
- v3 Linux links `#cgo linux pkg-config: gtk4 webkitgtk-6.0` by default
  ([linux_cgo.go](https://github.com/wailsapp/wails/blob/master/v3/pkg/application/linux_cgo.go));
  `-tags gtk3` selects `gtk+-3.0 webkit2gtk-4.1`
  ([linux_cgo_gtk3.go](https://github.com/wailsapp/wails/blob/master/v3/pkg/application/linux_cgo_gtk3.go)).
  Docs: the GTK3 path exists "for distributions that don't yet ship WebKitGTK 6.0 (Ubuntu 22.04
  LTS, Debian 12, Fedora <= 39, RHEL 9.x)" and "`-tags gtk3` will be removed in v3.1"
  ([Linux build guide](https://github.com/wailsapp/wails/blob/master/docs/src/content/docs/guides/build/linux.mdx)).
  v2 defaults to webkit2gtk-4.0 with `-tags webkit2_41` for 4.1
  ([v2 install docs](https://github.com/wailsapp/wails/blob/master/website/docs/gettingstarted/installation.mdx)).
  So a single Wails binary targets exactly one of three WebKitGTK ABIs; distro packages must
  pick one, and the default excludes current LTS distros.
- Tray: v3 `systemtray_linux.go` implements `/StatusNotifierItem` and `/StatusNotifierMenu`
  over `github.com/godbus/dbus/v5`, no appindicator library
  ([source](https://github.com/wailsapp/wails/blob/master/v3/pkg/application/systemtray_linux.go)).
  v2 has no tray API.
- AppImage: `wails3 package` runs linuxdeploy with an embedded `linuxdeploy-plugin-gtk.sh`, ldd's
  the binary to pick GTK 3 vs 4, and copies `WebKitWebProcess`, `WebKitNetworkProcess` and the
  injected-bundle `.so` into the AppDir
  ([appimage.go](https://github.com/wailsapp/wails/blob/master/v3/internal/commands/appimage.go)).
  Tauri does the same for Rust (copies the same three WebKit files, adds a gstreamer plugin)
  ([linuxdeploy.rs](https://github.com/tauri-apps/tauri/blob/dev/crates/tauri-bundler/src/bundle/linux/appimage/linuxdeploy.rs)),
  which is the state of the art: it works, but the AppImage carries a browser engine.
- Known Linux sharp edges documented by Wails itself: WebKit installs signal handlers that break
  Go panic recovery unless `runtime.ResetSignalHandlers()` is called per goroutine
  ([v2 Linux guide](https://github.com/wailsapp/wails/blob/master/website/docs/guides/linux.mdx));
  blank windows on NVIDIA proprietary drivers, worked around by setting
  `WEBKIT_DISABLE_DMABUF_RENDERER=1`; linuxdeploy's bundled `strip` cannot handle `.relr.dyn` on
  Arch/Fedora 39+/Ubuntu 24.04+ (Wails disables stripping)
  ([Linux build guide](https://github.com/wailsapp/wails/blob/master/docs/src/content/docs/guides/build/linux.mdx)).

### Fyne

- Wayland: since 2.8.0 (2026-07-11) "Wayland is now supported by default and will automatically
  be picked at runtime" ([CHANGELOG](https://github.com/fyne-io/fyne/blob/master/CHANGELOG.md)).
  Mechanism: `internal/build/driver_wayland.go` is `//go:build wayland && !x11`; the default
  (neither tag) build compiles both backends and asks GLFW which platform loaded
  ([window_x11wayland.go](https://github.com/fyne-io/fyne/blob/master/internal/driver/glfw/window_x11wayland.go)).
  Fyne is on `github.com/go-gl/glfw/v3.4` ([go.mod](https://github.com/fyne-io/fyne/blob/master/go.mod)),
  whose default Linux build "enables both X11 and Wayland" and "Added support for dynamic runtime
  switching between X11 and Wayland" ([go-gl/glfw README](https://github.com/go-gl/glfw)).
- Link-time deps (`v3.4/glfw/build.go`): `-lGL`, `-lX11 -lXrandr -lXxf86vm -lXi -lXcursor
  -lXinerama`, `-lwayland-client -lwayland-cursor -lwayland-egl -lxkbcommon`
  ([build.go](https://github.com/go-gl/glfw/blob/master/v3.4/glfw/build.go)); `go-gl/gl` adds
  `pkg-config: gl` ([procaddr.go](https://github.com/go-gl/gl/blob/master/v3.2-core/gl/procaddr.go)).
  GLFW additionally dlopens `libdecor-0.so.0` if present for Wayland decorations
  ([wl_init.c](https://github.com/go-gl/glfw/blob/master/v3.4/glfw/glfw/src/wl_init.c)), optional.
  Build hosts need `libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev wayland-protocols`
  ([go-gl/glfw README](https://github.com/go-gl/glfw)); Fyne's own Linux/NixOS prerequisite
  lists match ([docs source](https://github.com/fyne-io/developer.fyne.io/blob/master/started/index.md)).
- Tray: `fyne.io/systray` is a fork of getlantern/systray "removing the GTK dependency"; the Unix
  backend is pure godbus code exposing `org.kde.StatusNotifierItem` plus a `com.canonical.dbusmenu`
  menu ([systray_unix.go](https://github.com/fyne-io/systray/blob/master/systray_unix.go),
  [README](https://github.com/fyne-io/systray)). It is wired into Fyne as
  `desktop.App.SetSystemTrayMenu`. 2.8.1 fixed a "hidden SystrayMonitor window on KDE Wayland"
  (CHANGELOG), i.e. the Wayland tray path is exercised and maintained.
- Packaging: `fyne package -os linux` produces a `tar.xz` rooted at `usr/local/`, not an AppImage
  ([packaging docs](https://docs.fyne.io/started/packaging)); we build the AppImage ourselves
  (trivial, see below). Deb/AUR/Nix are ordinary `go build` with the dev headers present.
- Look: Fyne draws its own widgets with OpenGL and one theme on every platform; it will not look
  like a GNOME or KDE app. The 2.8.1 release also brought a "Big performance boost in repainting"
  (CHANGELOG), which addresses the most common complaint about Fyne rendering.

### Gio

- Deps: "development packages for: Wayland, x11, xkbcommon, GLES, EGL, libXcursor"; `nox11` and
  `nowayland` build tags; Vulkan driver optional ([install docs](https://gioui.org/doc/install/linux)).
  Link flags: `pkg-config: wayland-client wayland-cursor`
  ([os_wayland.go](https://github.com/gioui/gio/blob/main/app/os_wayland.go)),
  `pkg-config: egl wayland-egl` ([egl_wayland.go](https://github.com/gioui/gio/blob/main/app/egl_wayland.go)),
  `pkg-config: x11 xkbcommon xkbcommon-x11 x11-xcb xcursor xfixes`
  ([os_x11.go](https://github.com/gioui/gio/blob/main/app/os_x11.go)). GLES (`libGLESv2.so.2`)
  and Vulkan (`libvulkan.so.1`) are `dlopen`ed at runtime, not linked
  ([gl_unix.go](https://github.com/gioui/gio/blob/main/internal/gl/gl_unix.go),
  [vulkan.go](https://github.com/gioui/gio/blob/main/internal/vk/vulkan.go)). Lightest runtime
  footprint of the five.
- Model: "Gio is a library for implementing immediate mode user interfaces"
  ([architecture](https://gioui.org/doc/architecture)); widgets come from
  `gioui.org/widget/material` ("implements the Material design")
  ([pkg.go.dev](https://pkg.go.dev/gioui.org/widget/material)). Requires Go 1.24
  ([go.mod](https://github.com/gioui/gio/blob/main/go.mod)); still v0.x.
- Tray: no `StatusNotifier` code in `gioui/gio` or `gioui/gio-x` (GitHub code search, 0 hits);
  gio-x has `notify` (desktop notifications) only. `fyne.io/systray.RunWithExternalLoop` exists
  precisely for "another toolkit" that owns the main loop ([README](https://github.com/fyne-io/systray)).
- Smaller community (2.2k stars) and a very different API from retained-mode toolkits; the
  Bubble Tea TUI team would find the model familiar, but it is a real cost for contributors.

### gotk4 (GTK4 + libadwaita)

- Bindings track GTK 4.14 (branch `4`) and 4.16 ([README](https://github.com/diamondburned/gotk4));
  README: "memory leaks and sometimes crashes may occur in certain parts of the API, while other
  parts might be completely missing". gotk4-adwaita bindings are per-libadwaita-version branches
  (1.5 to 1.7) ([README](https://github.com/diamondburned/gotk4-adwaita)). Build needs
  `gtk4 gobject-introspection pkg-config` ([gotk4-examples](https://github.com/diamondburned/gotk4-examples)).
- Tray: GTK's `GtkStatusIcon` "has been deprecated in 3.14" in favour of `GNotification`
  ([GTK3 docs](https://docs.gtk.org/gtk3/class.StatusIcon.html)) and the GTK4 migration guide
  contains no tray API at all ([migrating-3to4](https://gitlab.gnome.org/GNOME/gtk/-/blob/main/docs/reference/gtk/migrating-3to4.md)).
  A GTK4 app needs the same godbus SNI code as everyone else.
- AppImage: must ship GTK4 + libadwaita + GLib schemas + pixbuf loaders + typelibs, which is what
  linuxdeploy-plugin-gtk does and it self-describes as "an (as of yet experimental) plugin"
  ([README](https://github.com/linuxdeploy/linuxdeploy-plugin-gtk)). Relying on host GTK4 instead
  reintroduces the version-skew problem (bindings pinned to 4.14/4.16).
- Look: best-in-class on GNOME; libadwaita is "Building blocks for modern GNOME applications"
  ([README](https://gitlab.gnome.org/GNOME/libadwaita)) and does not follow KDE or wlroots
  theming. The dev's own desktop (Hyprland) gains nothing from it.

### webview_go (bare webview binding)

- `webview.go` hard-codes `#cgo linux ... pkg-config: gtk+-3.0 webkit2gtk-4.0`
  ([source](https://github.com/webview/webview_go/blob/master/webview.go)); upstream `webview`
  supports 6.0/4.1/4.0 but the Go binding pins the oldest ABI, which Wails documents as already
  missing on Ubuntu 24.04. Last push 2024-08-31. No tray, no window management beyond one
  window, no packaging. Not viable.

## Recommendation detail: Fyne

Runtime dependency list for the `bnm` desktop binary (sonames, all present on any desktop
install of every mainstream distro; the AppImage excludelist already assumes `libGL.so.1`,
`libEGL.so.1`, `libX11.so.6`, `libwayland-client.so.0` and glibc on the host
([excludelist](https://github.com/AppImageCommunity/pkg2appimage/blob/master/excludelist))):

- glibc, `libGL.so.1`
- `libX11.so.6`, `libXrandr.so.2`, `libXxf86vm.so.1`, `libXi.so.6`, `libXcursor.so.1`, `libXinerama.so.1`
- `libwayland-client.so.0`, `libwayland-cursor.so.0`, `libwayland-egl.so.1`, `libxkbcommon.so.0`
- optional, dlopen'd: `libdecor-0.so.0` (Wayland CSD fallback on compositors without server-side decorations)
- D-Bus session bus for the tray (godbus, pure Go; no libdbus)

Build-time: `CGO_ENABLED=1`, gcc, and the dev headers above (`libgl1-mesa-dev xorg-dev
libwayland-dev libxkbcommon-dev wayland-protocols` on Debian; Fyne's docs give the Fedora, Arch and
NixOS `nix-shell` equivalents).

AppImage implications:

- AppDir = binary + `.desktop` + icon; run `linuxdeploy --appdir ... --output appimage` with no
  plugins. The small X libs (Xrandr, Xi, Xcursor, Xinerama, Xxf86vm, wayland-cursor,
  wayland-egl, xkbcommon) get bundled; the excludelisted ones stay on the host.
- Build the AppImage on the oldest supported base: "The ingredients used in your AppImage should
  not be built on a more recent base system than the oldest base system your AppImage is intended
  to run on" ([AppImage best practices](https://docs.appimage.org/reference/best-practices.html)).
  This is glibc-version hygiene and applies to any cgo binary; a CI container on the oldest LTS
  we support (or zig cc with a pinned glibc target) handles it.
- One AppImage covers Wayland and X11; no per-backend builds.

Why not the others, in one line each: Wails v3 is beta and ships a browser engine whose ABI
differs across the LTS distros we target; Gio is Fyne minus the tray and the retained-mode
widget set; gotk4 buys GNOME-native looks at the price of unstable bindings, no tray, and a
heavyweight AppImage; webview_go is unmaintained and pinned to the dying 4.0 ABI.

Open items for the decision ticket:

- Accept the Fyne look (own theme, not native) as a product decision, or budget for Wails v3 and
  a web front end plus a WebKitGTK dependency and 6.0-vs-4.1 packaging matrix.
- goreleaser: the desktop target needs native per-arch builds (or zig cc); confirm the release
  pipeline in the packaging ticket.
- The app must degrade gracefully with no SNI host (bare Hyprland, stock GNOME); design the
  window-first UX so the tray is an enhancement, not the entry point.
