{
  description = "bnm: a NetworkManager front end that doesn't suck";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      version = if self ? shortRev then "0.0.0-${self.shortRev}" else "0.0.0-dirty";
      moduleFor = import ./nix/module.nix;
    in
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        guiLibs = with pkgs; [
          libGL libglvnd libx11 libxcursor libxrandr libxinerama
          libxi libxxf86vm libxext libxfixes
          wayland wayland-protocols libxkbcommon
        ];
        # Everything the Tauri 2 desktop app needs at build time.
        tauriLibs = with pkgs; [ webkitgtk_4_1 gtk3 libayatana-appindicator librsvg libsoup_3 openssl glib-networking gdk-pixbuf cairo pango atk harfbuzz gsettings-desktop-schemas ];
        tauriTools = with pkgs; [ rustc cargo rustfmt clippy rust-analyzer cargo-tauri nodejs pnpm pkg-config ];
        bnm = pkgs.buildGoModule {
          pname = "bnm";
          inherit version;
          src = ./.;
          vendorHash = "sha256-8wVUACQmudCNl7FBrh/qpST7b2NpPVidhEql3rUhRak=";
          subPackages = [ "cmd/bnm" "cmd/bnmd" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X github.com/dopeCape/better-nm/internal/version.Version=${version}" ];
          postInstall = ''
            install -Dm644 packaging/systemd/bnmd.service $out/lib/systemd/user/bnmd.service
            substituteInPlace $out/lib/systemd/user/bnmd.service --replace-fail "/usr/bin/bnmd" "$out/bin/bnmd"
          '';
          meta = with pkgs.lib; { description = "NetworkManager front end: CLI, TUI, VPNs, monitoring"; license = licenses.mit; mainProgram = "bnm"; platforms = platforms.linux; };
        };
        bnm-desktop = pkgs.buildGoModule {
          pname = "bnm-desktop";
          inherit version;
          src = ./.;
          vendorHash = "sha256-8wVUACQmudCNl7FBrh/qpST7b2NpPVidhEql3rUhRak=";
          subPackages = [ "cmd/bnm-desktop" ];
          nativeBuildInputs = [ pkgs.pkg-config ];
          buildInputs = guiLibs;
          ldflags = [ "-s" "-w" ];
          postInstall = ''
            install -Dm644 packaging/desktop/bnm-desktop.desktop $out/share/applications/bnm-desktop.desktop
            install -Dm644 packaging/desktop/bnm.png $out/share/icons/hicolor/256x256/apps/bnm.png
          '';
        };
      in
      {
        packages = { default = bnm; inherit bnm bnm-desktop; };
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go gopls gotools golangci-lint pkg-config goreleaser nfpm
            python3 python3Packages.python-dbusmock python3Packages.dbus-python python3Packages.pygobject3
            dbus networkmanager iperf3 libnotify sqlite gnumake ] ++ guiLibs ++ tauriLibs ++ tauriTools;
          # A version manager (mise, asdf) may export GOROOT for another Go; the
          # shell's own go must own its GOROOT or `go build` mixes toolchains.
          shellHook = ''
            unset GOROOT
            export CGO_CFLAGS="-O2"
            export XDG_DATA_DIRS="${pkgs.gsettings-desktop-schemas}/share/gsettings-schemas/${pkgs.gsettings-desktop-schemas.name}:${pkgs.gtk3}/share/gsettings-schemas/${pkgs.gtk3.name}:$XDG_DATA_DIRS"
            export GIO_MODULE_DIR="${pkgs.glib-networking}/lib/gio/modules/"
            echo "bnm dev shell: go $(go version | cut -d' ' -f3), rustc $(rustc --version | cut -d' ' -f2), node $(node --version), tauri $(cargo tauri --version 2>/dev/null | cut -d' ' -f3)"
          '';
        };
      }) // {
      nixosModules.default = moduleFor { home = false; };
      homeModules.default = moduleFor { home = true; };
    };
}
