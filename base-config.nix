{ config, lib, pkgs, ... }:

{
  # Shared SSH/admin configuration used by both the live ISO and the MicroVM
  # so that both targets expose the exact same login surface.

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
    initialPassword = "";
  };

  # Wheel members can run sudo without entering a password.
  security.sudo.wheelNeedsPassword = false;

  # --- Nix -------------------------------------------------------------------
  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  system.stateVersion = "26.05";
}