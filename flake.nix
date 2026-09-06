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
          libGL libglvnd xorg.libX11 xorg.libXcursor xorg.libXrandr xorg.libXinerama
          xorg.libXi xorg.libXxf86vm xorg.libXext xorg.libXfixes
          wayland wayland-protocols libxkbcommon
        ];
        bnm = pkgs.buildGoModule {
          pname = "bnm";
          inherit version;
          src = ./.;
          vendorHash = null; # set by `nix build` failure output once go.sum is final
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
          vendorHash = null;
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
            dbus networkmanager iperf3 libnotify sqlite gnumake ] ++ guiLibs;
          shellHook = ''export CGO_CFLAGS="-O2"; echo "bnm dev shell: go $(go version | cut -d' ' -f3)"'';
        };
      }) // {
      nixosModules.default = moduleFor { home = false; };
      homeModules.default = moduleFor { home = true; };
    };
}
