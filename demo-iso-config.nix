{ config, lib, pkgs, modulesPath, ... }:

{
  imports = [
    # Makes `system.build.isoImage` available and produces a bootable,
    # writable (tmpfs root + overlay) live ISO. Unlike the full installer CD
    # modules it does not add a default `nixos` user or alter passwords.
    (modulesPath + "/installer/cd-dvd/iso-image.nix")
    # Shared SSH/admin configuration, identical to the plain ISO/MicroVM
    # targets.
    ./base-config.nix
    ./modules/desktop.nix
  ];

  isoImage.volumeID = "MICROBE-DEMO";

  # --- Networking -----------------------------------------------------------
  networking.networkmanager.enable = true;

  # --- Demo content -----------------------------------------------------------
  # Bakes microbe-demo/ into /etc/skel so it lands in the auto-login user's
  # home directory the moment that home is created on first boot.
  environment.etc."skel/microbe-demo".source = ./microbe-demo;

  microbe.demoDesktop = {
    enable = true;
    user = "admin";
    terminalCwd = "/home/admin/microbe-demo";
  };
}
