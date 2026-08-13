# finix-agent.nix — fixed flake-parts module shipped with microbe.
#
# finix's own analogue of agent.nix: builds and runs microbe-agent (the
# guest-side backend for `microbe exec`/`microbe shell`, ../agent/main.go)
# under finit instead of systemd. The vsock device itself is wired by
# finix-base.nix's cloud-hypervisor invocation (`--vsock cid=...,socket=
# notify.vsock`) -- the same hybrid-vsock UDS scheme nixos guests use, so
# this module only needs the guest-side transport module and the finit
# unit, not a device declaration of its own.
#
# Self-registers as flake.nixosModules.finix-agent (Dendritic pattern),
# pulled in by ServicePart's finix branch via
# config.flake.nixosModules.finix-agent.
{
  flake.nixosModules.finix-agent = { config, lib, pkgs, ... }:
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
      options.microCompose.serviceName = lib.mkOption {
        type = lib.types.str;
        description = "Service this configuration renders; set by parts/<svc>.nix.";
      };

      config = {
        # finix-base.nix's --vsock cid=...,socket=notify.vsock gives the
        # guest kernel a virtio-vsock PCI device, but CONFIG_VIRTIO_VSOCKETS
        # is built as a module (vmw_vsock_virtio_transport.ko), not built
        # in, and nothing auto-probes it: finix's modprobe.nix only
        # generates a modprobe.<mod> task per entry in boot.kernelModules
        # (verified live under the old QEMU vhost-vsock-pci device -- same
        # PCI-class device from the guest's point of view, same missing
        # module -- PF_VSOCK registered from the always-built core but the
        # transport driver never loaded and microbe-agent's listen silently
        # never came up until this was added).
        # vmw_vsock_virtio_transport is built-in (=y) via
        # CONFIG_VIRTIO_VSOCKETS_COMMON=y in microbe-kernel-6.12.config,
        # so no explicit modprobe is needed.

        # finit's analogue of agent.nix's systemd unit: respawn = true is
        # Restart=always; restart_sec is RestartSec.
        #
        # runlevels = "1234": start at runlevel 1, before any user services
        # (which are "234"). The host runner probes microbe-agent on vsock
        # 6969 to determine when to snapshot -- setting this to "1234" means
        # the snapshot is taken before user services start, so every restore
        # lands in a clean pre-user-service state. If left at the default
        # "234", the snapshot would include whatever state user services were
        # in when first booted, which could be buggy.
        finit.services.microbe-agent = {
          command = "${agentPkg}/bin/microbe-agent";
          runlevels = "1234";
          respawn = true;
          restart_sec = 1;
        };
      };
    };
}
