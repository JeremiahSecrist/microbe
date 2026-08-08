# guest-base.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.guest-base (Dendritic pattern), pulled
# in by per-service part files via config.flake.nixosModules.guest-base.
# Every service VM gets: ssh (key auth only), optimized microvm base, and no
# firewall on the managed networks (the bridge is host-controlled). The
# CLI's own keypair (internal/sshkey) is authorized for root so `microbe
# exec`/`microbe shell` can reach the guest; generated.json omits
# sshPublicKey until a keypair exists, so this is a no-op until then.
{
  flake.nixosModules.guest-base = { config, lib, pkgs, ... }:
    let
      generated = builtins.fromJSON (builtins.readFile ../generated.json);
    in
    {
      services.openssh = {
        enable = true;
        settings = {
          PasswordAuthentication = false;
          PermitRootLogin = "prohibit-password";
        };
      };

      users.users.root.openssh.authorizedKeys.keys =
        lib.optional (generated ? sshPublicKey) generated.sshPublicKey;

      networking.useDHCP = lib.mkDefault false;
      networking.firewall.enable = lib.mkDefault false;

      microvm.optimize.enable = lib.mkDefault true;
    };
}
