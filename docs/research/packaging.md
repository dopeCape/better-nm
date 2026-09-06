# Packaging a Go daemon plus desktop app: AppImage, AUR, deb, Nix

Research for issue #9. Feeds the release-pipeline decision (#19). Primary sources only; every
claim carries its URL. Docs fetched 2026-09-07. The desktop toolkit is still open (#3), so the
AppImage section covers both a GTK/libadwaita (cgo) app and a pure-Go (Fyne/Gio) app.

## Pipeline shape in one paragraph

One GitHub Actions workflow on tag push runs goreleaser: it builds the three binaries
(`bnmd`, `bnm`, `bnm-desktop`), wraps them with nfpm into deb/rpm/pacman packages, uploads
everything to the GitHub Release, then pushes a `bnm-bin` PKGBUILD to the AUR over SSH and,
optionally, a source-build `bnm` PKGBUILD. The AppImage is a separate job (linuxdeploy or
go-appimage) whose output goreleaser attaches to the release via `extra_files`. The Nix flake
lives in this repo and needs no publish step: a tag *is* the release, provided `vendorHash` was
kept in sync by CI. Every channel installs the same `/usr/lib/systemd/user/bnmd.service`; only
deb enables it by default, the rest print or self-install on first run.

## 1. goreleaser: multi-binary builds and cgo

- goreleaser supports a list of builds, each with its own `id`, `main` (path to the main
  package), and `binary`; the docs show a three-binary `./cmd/cli`, `./cmd/worker`,
  `./cmd/tracker` example. Since v2.15, `main: ./...` builds every `main` package in one
  build. https://goreleaser.com/customization/builds/builders/go/
- `env` is per build (`CGO_ENABLED=0` is the documented example, not a default); `overrides`
  set env/ldflags/tags per goos/goarch, "specially useful when using CGO".
  https://goreleaser.com/customization/builds/builders/go/
- cgo cross-compiling is not native: "Compiling with CGO is tricky, especially when
  cross-compiling. It requires extra setup and won't work 'out of the box'." Documented
  options: goreleaser Pro split/merge builds (native build per platform, "the recommended
  approach"), the `goreleaser/goreleaser-cross` Docker image with cross-compilers and a
  sysroot, or Zig as the C compiler ("might not work for all cases").
  https://goreleaser.com/resources/limitations/cgo/
- Consequence: `bnmd` and `bnm` (no cgo) cross-compile trivially; a GTK desktop binary needs
  a native runner per arch, or a sysroot with GTK4/libadwaita dev headers per arch inside
  goreleaser-cross. Fyne/Gio still need cgo for GL/X11/Wayland (see section 4), so the same
  constraint applies, only with far fewer headers.

## 2. deb and rpm via nfpm

- goreleaser's `nfpms` section produces "deb, .rpm, .apk, .ipk, Archlinux, and Windows .msix
  packages"; `formats: [deb, rpm, archlinux]` is enough for us. `ids` selects which builds go
  in; `bindir` defaults to `/usr/bin`. https://goreleaser.com/customization/package/nfpm/
- Relationship fields `dependencies`, `recommends`, `provides`, `conflicts`, `replaces` are
  overridable per format via `overrides.deb` / `overrides.rpm` (package names differ,
  e.g. `some-lib-dev` vs `some-lib-devel`). Same URL.
- `contents` entries copy arbitrary files: `src`/`dst`, `type: config|config|noreplace|
  symlink|dir|ghost|tree`, `file_info: {mode, owner, group}`, per-entry `packager:` filter.
  "GoReleaser will automatically add the binaries." There is no systemd special-casing; the
  unit is an ordinary entry `dst: /usr/lib/systemd/user/bnmd.service`, alongside
  `/usr/share/applications/bnm.desktop` and `/usr/share/icons/hicolor/...`. Same URL;
  nfpm reference https://nfpm.goreleaser.com/docs/configuration/
- `scripts: {preinstall, postinstall, preremove, postremove}` are paths, overridable per
  format. `templated_scripts` and `templated_contents` are Pro-only, so keep one static
  postinstall script that branches on the package manager at runtime.
  https://goreleaser.com/customization/package/nfpm/
- Signing: `deb.signature.key_file` (method `debsign` default; `dpkg-sig` "is not supported
  in newer Debian versions") and `rpm.signature.key_file`, both PGP; passphrase from
  `$NFPM_<ID>_<FORMAT>_PASSPHRASE`, `$NFPM_<ID>_PASSPHRASE`, then `$NFPM_PASSPHRASE`.
  https://goreleaser.com/customization/package/nfpm/ and
  https://nfpm.goreleaser.com/docs/configuration/
- `archlinux` format yields a `.pkg.tar.zst` for direct `pacman -U`; it is not what the AUR
  consumes. https://goreleaser.com/customization/package/nfpm/

## 3. AUR

Rules that bind us (https://wiki.archlinux.org/title/AUR_submission_guidelines):

- "Packages that use prebuilt deliverables, when the sources are available, must use the -bin
  suffix." "Packages that build from source using a specific version do not use a suffix."
  So the two pkgbases are `bnm` (source) and `bnm-bin` (prebuilt from the GitHub release).
- "The AUR only allows pushes to the master branch." `.SRCINFO` must be regenerated on every
  metadata change (`makepkg --printsrcinfo > .SRCINFO`), else "the AUR will not show updated
  version numbers".
- Authentication: "For write access to the AUR, you need to have an SSH key pair. The content
  of the public key needs to be copied to your profile in My Account". Pushes go to
  `ssh://aur@aur.archlinux.org/pkgbase.git`; the repo is created by the first clone/push. No
  HTTP or token push exists. Multiple public keys may be added to one account ("separating
  them with a newline"), so a dedicated CI key on the maintainer's account works without a
  second account.
- "Automated PKGBUILD updates are used at your own risk and any malfunctioning accounts and
  their packages may be removed without prior notice." Keep the bot on `skip_upload: auto`
  for prereleases and never push a broken .SRCINFO.
- Go build conventions (https://manual.archlinux.page/package-guidelines/go/): download
  modules in `prepare()` with `GOPATH="${srcdir}" go mod download -modcacherw`; export
  `CGO_CPPFLAGS/CFLAGS/CXXFLAGS/LDFLAGS` from the makepkg flags; `GOFLAGS="-buildmode=pie
  -trimpath -ldflags=-linkmode=external -mod=readonly -modcacherw"`; `makedepends=(go)`.

goreleaser publishers:

- `aurs` (OSS) generates and pushes a `-bin` PKGBUILD: "GoReleaser will enforce a `-bin`
  suffix if its not present." Needs `private_key` ("can either be a path or the key contents.
  IMPORTANT: the key must not be password-protected") and `git_url:
  ssh://aur@aur.archlinux.org/bnm-bin.git`. Override the default `package:` script, which
  only installs the binary, to also install the unit, desktop file, icon, license and
  completions; `install: ./scripts/bnm.install` adds a pacman `.install` file for
  `post_install` hooks. `provides`/`conflicts` default to the project name.
  https://goreleaser.com/customization/publish/aur/
- `aur_sources` (OSS, since v2.5) pushes a source PKGBUILD built from goreleaser's source
  archive (`source.enabled: true` required); `makedepends` default `["go", "git"]`, custom
  `prepare`/`build`/`package` scripts, `arches` default `[x86_64, aarch64]`.
  https://goreleaser.com/customization/publish/aursources/
- Whether goreleaser writes `.SRCINFO` is not stated on either page; verify on the first dry
  run with `skip_upload: true` (output lands in `dist/`).

## 4. AppImage

AppDir contract and tooling:

- An AppDir MUST contain `AppRun`, exactly one top-level `.desktop`, `.DirIcon`, and the icon
  named by `Icon=`; `usr/bin`, `usr/lib`, `usr/share/*` are conventions.
  https://docs.appimage.org/reference/appdir.html
- `appimagetool AppDir` squashes it and prepends the runtime; the current appimagetool
  downloads the runtime from `AppImage/type2-runtime` releases unless `--runtime-file` is
  given (needed for offline/hermetic builds). https://github.com/AppImage/appimagetool
- type2-runtime is "statically linked ... libfuse2 is no longer required on the target
  system"; it uses fuse3 and honours `APPIMAGE_EXTRACT_AND_RUN`.
  https://github.com/AppImage/type2-runtime
- Self-update metadata: `-u "gh-releases-zsync|dopeCape|better-nm|latest|bnm-*x86_64.AppImage.zsync"`
  embeds the string and writes the `.zsync` file to upload next to the AppImage.
  https://github.com/AppImage/AppImageSpec/blob/master/draft.md#github-releases
- Build-host rule: "The ingredients used in your AppImage should not be built on a more recent
  base system than the oldest base system your AppImage is intended to run on" because glibc
  is never bundled. https://docs.appimage.org/reference/best-practices.html
  In practice: build the AppImage job in a container of the oldest supported Ubuntu LTS.

How runtime libs get bundled:

- The excludelist enumerates libs "assumed to be present on the host system and hence should
  NOT be bundled": glibc, `libGL.so.1`, `libEGL.so.1`, `libdrm`, `libgbm`, `libX11`, `libxcb`,
  `libwayland-client`, `libfontconfig`, `libfreetype`, `libasound`. Not on it (so tools bundle
  them): `libxkbcommon`, `libvulkan`, `libwayland-egl`, all of GLib/GTK/gdk-pixbuf.
  https://github.com/AppImageCommunity/pkg2appimage/blob/master/excludelist
- linuxdeploy walks `ldd` output, copies non-excluded libs into `usr/lib`, patches rpath to
  `$ORIGIN/../lib` with patchelf, strips, and bakes the excludelist in at build time.
  `linuxdeploy --appdir AppDir -e bnm-desktop -d bnm.desktop -i bnm.png --output appimage`.
  https://github.com/linuxdeploy/linuxdeploy and
  https://docs.appimage.org/packaging-guide/from-source/native-binaries.html
- **Pure-Go toolkit (Fyne/Gio):** cgo links only GL/EGL, X11/xcb, wayland-client, xkbcommon,
  vulkan (Fyne deps: `libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev`,
  https://docs.fyne.io/started/; Gio: https://gioui.org/doc/install/linux). Everything but
  `libxkbcommon`/`libvulkan` is host-provided, so the AppDir is essentially binary + desktop +
  icon + AppRun. Neither toolkit ships an AppImage builder: `fyne package -os linux` makes a
  tar.xz with a Makefile (https://docs.fyne.io/started/packaging), fyne-cross targets tar.xz
  only, and `gogio` has no Linux target (its `main.go` cases are ios/tvos/android/js/
  windows/macos, https://github.com/gioui/gio-cmd).
- **GTK4/libadwaita (gotk4):** use `linuxdeploy --plugin gtk`. The plugin detects the GTK
  version (GTK 4 supported), copies the GTK libs, GLib schemas (+ `glib-compile-schemas`),
  gdk-pixbuf loaders + cache, typelibs, Pango/librsvg, and installs an `apprun-hooks` script
  exporting `GSETTINGS_SCHEMA_DIR`, `GDK_PIXBUF_MODULE_FILE`, `GTK_PATH`, `XDG_DATA_DIRS`.
  https://github.com/linuxdeploy/linuxdeploy-plugin-gtk
  Two caveats from the same source: the hook forces `GDK_BACKEND=x11` ("Crash with Wayland
  backend on Wayland"), and libadwaita is never named in the script (it is only copied as an
  ordinary ldd dependency); issue #60 asking about GTK4/libadwaita support is open, and the
  AppImage maintainer said in Dec 2024 "You'd probably need something like
  linuxdeploy-plugin-gtk but for Gtk 4 and libadwaita and I don't know whether anyone has made
  such a thing" (https://github.com/orgs/AppImage/discussions/1374). A GTK4 AppImage is
  therefore feasible but needs hand-tuning and a Wayland test on Hyprland/sway.
- go-appimage (`appimagetool deploy usr/share/applications/*.desktop`) is the alternative:
  obeys the excludelist, has built-in GTK 2/3/4 handling, `-s` bundles even glibc + ld.so
  (runs on hosts older than the build box), and on GitHub Actions auto-embeds
  `gh-releases-zsync` update info. https://github.com/probonopd/go-appimage/blob/master/src/appimagetool/README.md
- "AppImages are standalone bundles, and do not need to be installed"; the docs never
  mention systemd, so an AppImage registers a unit only by writing it itself (section 6).
  https://docs.appimage.org/user-guide/run-appimages.html

## 5. Nix flake

- `buildGoModule` needs `pname`, `version`, `src`, and `vendorHash`, the hash of the
  intermediate `goModules` fixed-output derivation. Obtain it with `vendorHash =
  lib.fakeHash;` and reading the mismatch from the failed build. `vendorHash = null` means
  "use the `vendor` directory in the source repo" (commit `go mod vendor` output and never
  update the hash). https://nixos.org/manual/nixpkgs/stable/#sec-language-go
- Useful attrs: `subPackages = [ "cmd/bnmd" "cmd/bnm" "cmd/bnm-desktop" ]`, `ldflags = [ "-s"
  "-w" "-X main.version=${version}" ]`, `tags`, `env.CGO_ENABLED` (defaults to 1), `doCheck`,
  `nativeBuildInputs = [ pkg-config installShellFiles copyDesktopItems ]`. Same URL and
  https://github.com/NixOS/nixpkgs/blob/master/pkgs/build-support/go/module.nix
- GTK4 app: add `wrapGAppsHook4` ("for GTK 4 apps") to `nativeBuildInputs`, `gtk4 libadwaita`
  to `buildInputs`. https://github.com/NixOS/nixpkgs/blob/master/doc/languages-frameworks/gnome.section.md
  Precedent for a Go GTK4/libadwaita app in nixpkgs:
  https://github.com/NixOS/nixpkgs/blob/master/pkgs/by-name/vi/vinegar/package.nix; for Fyne
  (`libGL libx11 libxcursor libxinerama libxi libxrandr libxxf86vm`):
  https://github.com/NixOS/nixpkgs/blob/master/pkgs/by-name/fy/fyne/package.nix
- Flake shape: `packages.${system}.default = pkgs.buildGoModule { src = self; ... }` is the
  manual's own example pattern. `self.rev`/`self.shortRev`/`self.lastModifiedDate` are the
  only version inputs; `.git` is not in the store copy, so `git describe` cannot run in the
  build. Export `nixosModules.default` and `homeModules.default` (the name `nix flake check`
  recognises; `homeManagerModules` triggers "unknown flake output").
  https://nix.dev/manual/nix/latest/command-ref/new-cli/nix3-flake.html and
  https://github.com/NixOS/nix/blob/master/src/nix/flake.cc
- How a release rides: consumers pin `github:dopeCape/better-nm/v1.2.3` ("`<rev-or-ref>`
  specifies the name of a branch or tag"); GitHub flakes "are downloaded as tarball
  archives, rather than through Git". So there is no publish step; the obligation is that
  every commit touching `go.mod`/`go.sum` also updates `vendorHash`, which CI enforces by
  running `nix build` on PRs. Same nix3-flake URL.
- goreleaser's `nix` publisher (binary derivation into a NUR repo; "not supported:
  Generating packages that compile from source (using buildGoModule)"; needs `nix-hash` and
  a PAT) is redundant with the in-repo flake. https://goreleaser.com/customization/publish/nix/

## 6. Shipping and enabling the systemd user unit, per channel

Common facts (https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html,
systemd.special.html, systemctl.html):

- Vendor path is `/usr/lib/systemd/user` ("User units installed by the distribution package
  manager"); admin path `/etc/systemd/user`; per-user `~/.config/systemd/user`.
- User manager `default.target` "is the main target of the user service manager, started by
  default", so `[Install] WantedBy=default.target`. Use `graphical-session.target` +
  `PartOf=` only if the daemon must die with the desktop; bnmd should not, so `default.target`.
- `systemctl --global enable` operates on "the global user configuration directory, thus
  enabling ... for all future logins of all users". `systemctl --user enable --now` for the
  current user. `loginctl enable-linger` keeps the manager alive without a session.

Per channel:

- **deb:** debhelper's `dh_installsystemduser` installs to `usr/lib/systemd/user/` and its
  postinst calls `deb-systemd-helper --user enable`, which runs `systemctl --global
  --preset-mode=enable-only preset UNIT` on first install. nfpm does not run debhelper, so
  our postinstall reproduces it: `deb-systemd-helper --user enable bnmd.service` (from
  `init-system-helpers`, always present) plus `deb-systemd-invoke --user start bnmd.service`
  to start it for already-logged-in users (systemd >= 249).
  https://manpages.debian.org/testing/debhelper/dh_installsystemduser.1.en.html and
  https://sources.debian.org/src/init-system-helpers/latest/script/deb-systemd-helper/
- **rpm:** Fedora's `%systemd_user_post` expands to `systemd-update-helper install-user-units`
  = `systemctl --no-reload preset --global UNIT`; that enables nothing on Fedora unless a
  preset says so, and Fedora policy is "must be covered by the Fedora preset policy" for
  enable-by-default. For a third-party rpm, ship `/usr/lib/systemd/user-preset/90-bnm.preset`
  containing `enable bnmd.service` (files sort lexicographically; 90 beats a 99-default) and
  call the preset command in postinstall. https://docs.fedoraproject.org/en-US/packaging-guidelines/Scriptlets/,
  https://github.com/systemd/systemd/blob/main/src/rpm/systemd-update-helper.in,
  https://www.freedesktop.org/software/systemd/man/latest/systemd.preset.html
- **Arch (AUR and pacman pkg):** ship the unit at `/usr/lib/systemd/user/`; do not enable.
  Arch "does not make use of systemd presets, and does not enable most services on
  installation"; the etiquette rule is that setup directions "should be echoed during install
  using an .install file", so `post_install` prints `systemctl --user enable --now
  bnmd.service`. https://wiki.archlinux.org/title/Systemd,
  https://wiki.archlinux.org/title/Arch_package_guidelines,
  https://wiki.archlinux.org/title/Systemd/User
- **Nix:** NixOS scans `$out/lib/systemd/user/*` of every package in `systemd.packages` (not
  `share/systemd/user`), but skips `.wants` symlinks, so the flake's `nixosModules.default`
  must also set `systemd.user.services.bnmd.wantedBy = [ "default.target" ]` ("creates a
  .wants symlink ... without the need for running systemctl enable"; `[Install]` is ignored).
  home-manager: `systemd.user.services.bnmd = { Install.WantedBy = [ "default.target" ]; ...}`
  and `systemd.user.startServices` (default `true`, sd-switch) starts it on activation.
  https://github.com/NixOS/nixpkgs/blob/master/nixos/lib/systemd-lib.nix,
  https://github.com/NixOS/nixpkgs/blob/master/nixos/lib/systemd-unit-options.nix,
  https://nix-community.github.io/home-manager/options.xhtml#opt-systemd.user.services
- **AppImage / tarball:** nothing installs anything; the binary itself must offer
  `bnm daemon install`, writing `~/.config/systemd/user/bnmd.service` with
  `ExecStart=<abs path> daemon`, then `systemctl --user daemon-reload && systemctl --user
  enable --now bnmd.service`. Inside an AppImage the path is `$APPIMAGE` ("(Absolute) path to
  AppImage file (with symlinks resolved)"), never `/proc/self/exe` (that is inside the
  mount). Prefer writing the file over `systemctl --user link`, whose man page forbids units
  "underneath /home/" unless on the root filesystem. Re-check on every launch and rewrite
  when `$APPIMAGE` moved. https://docs.appimage.org/packaging-guide/environment-variables.html,
  https://www.freedesktop.org/software/systemd/man/latest/systemctl.html

## 7. CI runners, secrets, and what GitHub Actions cannot do

- goreleaser-action: `on.push.tags`, `permissions: contents: write`, `actions/checkout` with
  `fetch-depth: 0` ("required for GoReleaser"), `goreleaser/goreleaser-action` with
  `args: release --clean`. "The action does not install, configure or authenticate into
  dependencies": GTK dev headers, nfpm's PGP key import, `nix-hash` are our job.
  https://goreleaser.com/customization/ci/actions/
- Secrets: `AUR_KEY` (unencrypted SSH private key registered on the AUR account), a PGP key +
  `NFPM_PASSPHRASE` for deb/rpm signing (import via `crazy-max/ghaction-import-gpg`), and
  nothing else if the Nix flake stays in-repo. `GITHUB_TOKEN` cannot push to another repo;
  a NUR or separate tap would need a PAT. Same URL.
- Runners: `ubuntu-latest` (x86_64) and `ubuntu-24.04-arm` / `ubuntu-22.04-arm` (arm64,
  "available for free in public repositories") give native builds for both arches, which is
  what cgo needs. https://docs.github.com/en/actions/reference/runners/github-hosted-runners,
  https://github.blog/changelog/2025-01-16-linux-arm64-hosted-runners-now-available-for-free-in-public-repositories-public-preview/
- Nix on CI: `DeterminateSystems/nix-installer-action` or `cachix/install-nix-action`, then
  `nix build .#default` and `nix flake check` per arch (use the arm runner; binfmt emulation
  is NixOS-only). https://github.com/DeterminateSystems/nix-installer-action
- AppImage job: container from the oldest supported LTS (section 4), pre-download the
  type2-runtime for `--runtime-file`, one job per arch, upload `.AppImage` + `.zsync` as
  goreleaser `extra_files` (https://goreleaser.com/customization/publish/scm/).
- Not automatable from Actions: creating the AUR account and pasting the CI public key into
  it; generating and safekeeping the PGP signing key; the very first AUR push (creates the
  pkgbase; identical mechanics, but do it by hand to check `.SRCINFO`); and any Flathub/
  Snap/distro-repo submission, which are review processes. Everything else, including the
  Nix flake, is tag-driven.

## Open points for #19

- Toolkit choice (#3) decides whether the AppImage needs `linuxdeploy-plugin-gtk` (with its
  x11-forcing hook and unverified libadwaita coverage) or is a near-bare binary.
- Enable-by-default policy: deb yes (Debian convention), rpm via our own preset file, Arch
  never, Nix declarative, AppImage self-install. Decide whether `bnm daemon install` is also
  the fallback for every channel when the user unit is not enabled.
- Source AUR package (`aur_sources`) doubles maintenance; decide whether `-bin` alone is v1.
