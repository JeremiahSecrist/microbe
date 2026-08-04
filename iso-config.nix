{ config, lib, pkgs, nixpkgs, ... }:

{
  imports = [
    # Makes `system.build.isoImage` available and produces a bootable,
    # writable (tmpfs root + overlay) live ISO. Unlike the full installer CD
    # modules it does not add a default `nixos` user or alter passwords.
    (nixpkgs + "/nixos/modules/installer/cd-dvd/iso-image.nix")
  ];

  isoImage.volumeID = "nixos-ssh";

  # --- Networking -----------------------------------------------------------
  # NetworkManager so the live environment can connect (Ethernet / wifi).
  networking.networkmanager.enable = true;

  # --- SSH ------------------------------------------------------------------
  services.openssh = {
    enable = true;
    settings = {
      # Allow the root account to log in over SSH with a password.
      PermitRootLogin = "yes";
      PasswordAuthentication = true;
    };
    # Open port 22 in the (enabled by default) firewall.
    openFirewall = true;
  };

  # --- Users ----------------------------------------------------------------
  # Root logs in over SSH with the password "root".
  users.users.root.initialPassword = "root";

  # Admin user: in the wheel group, no password set. Combined with
  # `security.sudo.wheelNeedsPassword = false` this allows passwordless sudo.
  # It cannot log in over SSH without an SSH key (no password), which is the
  # intended "no password" behavior.
  users.users.admin = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    description = "Administrator";
  };

  # Wheel members can run sudo without entering a password.
  security.sudo.wheelNeedsPassword = false;

  system.stateVersion = "24.11";
}
