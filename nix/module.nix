# One module body for both NixOS (services.bnm) and Home Manager (services.bnm).
# NixOS: installs the package and a systemd user unit for every user.
# Home Manager: installs the package and enables the unit for that user.
# `self` is the flake, so the package options default to this flake's own builds.
{ self, home }:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.bnm;
  pkg = cfg.package;
  flakePkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
in
{
  options.services.bnm = {
    enable = lib.mkEnableOption "bnm, the NetworkManager front end daemon";
    package = lib.mkOption {
      type = lib.types.package;
      default = flakePkgs.bnm;
      defaultText = lib.literalExpression "bnm.packages.\${system}.bnm";
      description = "The bnm package (provides bnm and bnmd).";
    };
    desktop = lib.mkOption { type = lib.types.bool; default = false; description = "Also install the desktop app (Tauri)."; };
    desktopPackage = lib.mkOption {
      type = lib.types.package;
      default = flakePkgs.bnm-desktop;
      defaultText = lib.literalExpression "bnm.packages.\${system}.bnm-desktop";
      description = "The desktop app package, installed when `desktop` is true.";
    };
  };

  config = lib.mkIf cfg.enable (
    let
      unit = {
        Unit = { Description = "bnm network daemon"; After = [ "network.target" ]; };
        Service = { ExecStart = "${pkg}/bin/bnmd"; Restart = "on-failure"; RestartSec = 2; };
        Install = { WantedBy = [ "default.target" ]; };
      };
      pkgsList = [ pkg ] ++ lib.optional cfg.desktop cfg.desktopPackage;
    in
    if home then {
      home.packages = pkgsList;
      systemd.user.services.bnmd = unit;
    } else {
      environment.systemPackages = pkgsList;
      systemd.user.services.bnmd = {
        description = "bnm network daemon";
        after = [ "network.target" ];
        wantedBy = [ "default.target" ];
        serviceConfig = { ExecStart = "${pkg}/bin/bnmd"; Restart = "on-failure"; RestartSec = 2; };
      };
    }
  );
}
