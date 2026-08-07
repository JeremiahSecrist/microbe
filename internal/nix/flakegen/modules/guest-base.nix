# guest-base.nix — fixed module shipped with microbe.
#
# Every service VM gets: ssh (key auth only), optimized microvm base, and no
# firewall on the managed networks (the bridge is host-controlled).
{ config, lib, pkgs, ... }:
{
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      PermitRootLogin = "prohibit-password";
    };
  };

  networking.useDHCP = lib.mkDefault false;
  networking.firewall.enable = lib.mkDefault false;

  microvm.optimize.enable = lib.mkDefault true;
}
