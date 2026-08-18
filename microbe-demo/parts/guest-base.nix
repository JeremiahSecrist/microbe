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

      # --- systemd services that add zero value in a headless service microVM ---
      # All new options use lib.mkDefault so an individual service can re-enable
      # anything it genuinely needs.

      # journald: keep volatile (RAM) logging but cap hard at 16M. Default is
      # unbounded in-memory buffering which can balloon under log spam.
      services.journald.extraConfig = ''
        Storage=volatile
        RuntimeMaxUse=16M
      '';

      # logind: manages interactive login sessions. microbe exec/shell reaches
      # guests via vsock, not PAM sessions. mkDefault so a service that genuinely
      # needs session tracking (unusual) can re-enable.
      services.logind.enable = lib.mkDefault false;

      # resolved: renderer.nix writes a static /etc/resolv.conf via
      # networking.nameservers; no stub resolver needed. mkOverride 999 beats
      # networkd.nix's lib.mkDefault true (priority 1000) while still yielding
      # to an explicit lib.mkForce in a service that genuinely needs resolved.
      services.resolved.enable = lib.mkOverride 999 false;

      # nix daemon: never used inside service VMs. Saves the daemon process +
      # inotify watches.
      nix.enable = lib.mkDefault false;

      # coredump collection: writing cores to a tmpfs that's discarded on VM exit
      # is pointless and wastes RAM on crashes.
      systemd.coredump.enable = lib.mkDefault false;
    };
}
