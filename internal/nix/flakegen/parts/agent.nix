# agent.nix — fixed flake-parts module shipped with microbe.
#
# Builds and runs microbe-agent, the guest-side backend for `microbe
# exec`/`microbe shell`: it listens on a fixed AF_VSOCK port (see
# ../agent/main.go) and runs whatever command the host sends, reached
# through cloud-hypervisor's vsock device rather than any guest-visible
# network service. Replaces the sshd-based transport entirely -- no
# authorized_keys, no listening network port, no guest reachability
# requirement at all (see internal/cmd's exec.go/shell.go and
# internal/vsockexec).
#
# Self-registers as flake.nixosModules.agent (Dendritic pattern), pulled in
# by per-service part files via config.flake.nixosModules.agent.
{
  flake.nixosModules.agent = { config, lib, pkgs, ... }:
    let
      agentPkg = pkgs.buildGoModule {
        pname = "microbe-agent";
        version = "1";
        src = ../agent;
        vendorHash = null;
        env.CGO_ENABLED = 0;
      };
    in
    {
      systemd.services.microbe-agent = {
        description = "microbe exec/shell backend (vsock)";
        wantedBy = [ "multi-user.target" ];
        after = [ "network.target" ];
        serviceConfig = {
          ExecStart = "${agentPkg}/bin/microbe-agent";
          Restart = "always";
          RestartSec = "1";
        };
      };
    };
}
