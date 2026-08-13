# guest-base.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.guest-base (Dendritic pattern), pulled
# in by per-service part files via config.flake.nixosModules.guest-base.
# Every service VM gets: optimized microvm base, and no firewall on the
# managed networks (the bridge is host-controlled). `microbe exec`/`microbe
# shell` reach the guest through microbe-agent over vsock (see
# parts/agent.nix), not sshd -- no authorized key, no listening network
# port, no guest network reachability requirement at all.
{
  flake.nixosModules.guest-base = { config, lib, pkgs, ... }:
    {
      networking.useDHCP = lib.mkDefault false;
      networking.firewall.enable = lib.mkDefault false;

      microvm.optimize.enable = lib.mkDefault true;

      system.stateVersion = lib.mkDefault "26.05";
    };
}
