# NixOS module for a KDE Plasma desktop that auto-logs-in a user and opens a
# terminal cd'd into a given directory. Built for the microbe demo ISO; the
# target user/directory are configurable so other images can reuse it.
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.microbe.demoDesktop;
in
{
  options.microbe.demoDesktop = {
    enable = mkOption {
      type = types.bool;
      default = false;
      description = "Enable a KDE Plasma desktop that auto-logs-in and autostarts a terminal.";
    };

    user = mkOption {
      type = types.str;
      default = "admin";
      description = "User to auto-login as and to autostart the terminal for.";
    };

    terminalCwd = mkOption {
      type = types.str;
      description = "Directory the autostarted terminal should open in.";
    };
  };

  config = mkIf cfg.enable {
    services.xserver.enable = true;
    services.desktopManager.plasma6.enable = true;
    services.displayManager.sddm.enable = true;
    services.displayManager.autoLogin = {
      enable = true;
      user = cfg.user;
    };

    # Autostarts on every Plasma session for every user; fine here since the
    # ISO only ever logs in `cfg.user`.
    environment.etc."xdg/autostart/microbe-demo-terminal.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=microbe demo terminal
      Exec=konsole --workdir ${cfg.terminalCwd}
      X-KDE-autostart-phase=2
    '';
  };
}
