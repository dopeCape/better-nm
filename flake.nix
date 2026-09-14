{
  description = "bnm: a NetworkManager front end that doesn't suck";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      version = if self ? shortRev then "0.0.0-${self.shortRev}" else "0.0.0-dirty";
      moduleFor = home: import ./nix/module.nix { inherit self home; };
    in
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        # Everything the Tauri 2 desktop app needs at build time.
        tauriLibs = with pkgs; [ webkitgtk_4_1 gtk3 libayatana-appindicator librsvg libsoup_3 openssl glib-networking gdk-pixbuf cairo pango atk harfbuzz gsettings-desktop-schemas ];
        tauriTools = with pkgs; [ rustc cargo rustfmt clippy rust-analyzer cargo-tauri nodejs pnpm pkg-config ];
        bnm = pkgs.buildGoModule {
          pname = "bnm";
          inherit version;
          src = ./.;
          vendorHash = "sha256-VSDMLbo73ludnsOFQS95Xn4iwETDWFwenINPkef23qg=";
          subPackages = [ "cmd/bnm" "cmd/bnmd" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X github.com/dopeCape/better-nm/internal/version.Version=${version}" ];
          postInstall = ''
            install -Dm644 packaging/systemd/bnmd.service $out/lib/systemd/user/bnmd.service
            substituteInPlace $out/lib/systemd/user/bnmd.service --replace-fail "/usr/bin/bnmd" "$out/bin/bnmd"
          '';
          meta = with pkgs.lib; { description = "NetworkManager front end: CLI, TUI, VPNs, monitoring"; license = licenses.mit; mainProgram = "bnm"; platforms = platforms.linux; };
        };
        # The Tauri 2 desktop app: Rust shell (desktop/src-tauri) + React frontend (desktop/src).
        # cargo-tauri.hook runs `cargo tauri build --bundles deb` and installs the deb's
        # usr/ tree (binary, .desktop, hicolor icons) into $out. The frontend is built in
        # preBuild from the offline pnpm store, so tauri's own beforeBuildCommand is blanked.
        bnm-desktop = pkgs.rustPlatform.buildRustPackage (finalAttrs: {
          pname = "bnm-desktop";
          inherit version;
          src = ./.;
          cargoRoot = "desktop/src-tauri";
          buildAndTestSubdir = "desktop/src-tauri";
          cargoLock.lockFile = ./desktop/src-tauri/Cargo.lock;
          pnpmRoot = "desktop";
          pnpmDeps = pkgs.fetchPnpmDeps {
            pname = "${finalAttrs.pname}-frontend";
            inherit (finalAttrs) version;
            src = ./desktop;
            fetcherVersion = 4;
            hash = "sha256-cSB87Pw5NyDI/458+VSMHP5Txb+N2Kd6zEPmJyvEE5M=";
          };
          nativeBuildInputs = with pkgs; [ pkg-config wrapGAppsHook3 cargo-tauri.hook nodejs pnpm pnpmConfigHook ];
          buildInputs = with pkgs; [ webkitgtk_4_1 gtk3 libayatana-appindicator librsvg libsoup_3 openssl ];
          preBuild = ''
            (cd desktop && pnpm build)
          '';
          tauriBuildFlags = [ "--config" (builtins.toJSON { build.beforeBuildCommand = ""; }) ];
          # cargo test is CI's job (the live test needs a Go toolchain to build bnmd --fake).
          doCheck = false;
          postInstall = ''
            test -x $out/bin/bnm-desktop
            test -f $out/share/applications/bnm-desktop.desktop
          '';
          meta = with pkgs.lib; { description = "bnm desktop: Wi-Fi, VPNs and network quality in one place"; license = licenses.mit; mainProgram = "bnm-desktop"; platforms = platforms.linux; };
        });
      in
      {
        packages = { default = bnm; inherit bnm bnm-desktop; };
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go gopls gotools golangci-lint pkg-config goreleaser nfpm
            python3 python3Packages.python-dbusmock python3Packages.dbus-python python3Packages.pygobject3
            dbus networkmanager iperf3 libnotify sqlite gnumake ] ++ tauriLibs ++ tauriTools;
          # A version manager (mise, asdf) may export GOROOT for another Go; the
          # shell's own go must own its GOROOT or `go build` mixes toolchains.
          shellHook = ''
            unset GOROOT
            export XDG_DATA_DIRS="${pkgs.gsettings-desktop-schemas}/share/gsettings-schemas/${pkgs.gsettings-desktop-schemas.name}:${pkgs.gtk3}/share/gsettings-schemas/${pkgs.gtk3.name}:$XDG_DATA_DIRS"
            export GIO_MODULE_DIR="${pkgs.glib-networking}/lib/gio/modules/"
            echo "bnm dev shell: go $(go version | cut -d' ' -f3), rustc $(rustc --version | cut -d' ' -f2), node $(node --version), tauri $(cargo tauri --version 2>/dev/null | cut -d' ' -f3)"
          '';
        };
      }) // {
      nixosModules.default = moduleFor false;
      homeModules.default = moduleFor true;
    };
}
