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
  isoImage.makeEfiBootable = true;
  isoImage.makeUsbBootable = true;

  # --- Networking -----------------------------------------------------------
  networking.networkmanager.enable = true;

  # --- Demo content -----------------------------------------------------------
  # NixOS's declarative `createHome` just makes an empty directory -- unlike
  # a traditional distro it does not copy /etc/skel into new homes. Copy
  # microbe-demo/ into place ourselves via tmpfiles, which runs after the
  # admin home directory has been created by system activation.
  systemd.tmpfiles.rules = [
    "C /home/admin/microbe-demo - admin users - ${./microbe-demo}"
  ];

  microbe.demoDesktop = {
    enable = true;
    user = "admin";
    terminalCwd = "/home/admin/microbe-demo";
  };
}
