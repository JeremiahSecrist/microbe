{ config, lib, pkgs, modulesPath, ... }:

{
  imports = [
    # Makes `system.build.isoImage` available and produces a bootable,
    # writable (tmpfs root + overlay) live ISO. Unlike the full installer CD
    # modules it does not add a default `nixos` user or alter passwords.
    (modulesPath + "/installer/cd-dvd/iso-image.nix")
    # Shared SSH/admin configuration, identical to the MicroVM target.
    ./base-config.nix
  ];

  isoImage.volumeID = "nixos-ssh";

  # --- Networking -----------------------------------------------------------
  # NetworkManager so the live environment can connect (Ethernet / wifi).
  networking.networkmanager.enable = true;
}