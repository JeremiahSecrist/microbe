# finix-agent.nix — fixed flake-parts module shipped with microbe.
#
# finix's own analogue of agent.nix: builds and runs microbe-agent (the
# guest-side backend for `microbe exec`/`microbe shell`, ../agent/main.go)
# under finit instead of systemd, and wires the vsock device itself onto
# the QEMU command line -- unlike the nixos/cloud-hypervisor path, finix
# guests boot under plain QEMU (see finix-base.nix) with no vsock device
# by default, so there's nothing for microbe-agent to listen on without
# this module adding one explicitly.
#
# Reaches the guest over real kernel AF_VSOCK (vhost-vsock-pci), not
# cloud-hypervisor's hybrid-vsock UDS scheme: plain QEMU's vhost-vsock-pci
# needs no companion -chardev (unlike virtio-serial) but does need the
# host's vhost_vsock kernel module loaded and /dev/vhost-vsock accessible
# to the user running `microbe up` -- `modprobe vhost_vsock` if `microbe
# shell`/`exec` fails to connect (internal/vsockexec.DialVsock's error
# will name the missing device).
#
# Self-registers as flake.nixosModules.finix-agent (Dendritic pattern),
# pulled in by ServicePart's finix branch via
# config.flake.nixosModules.finix-agent.
{
  flake.nixosModules.finix-agent = { config, lib, pkgs, ... }:
    let
      # Own copy of renderer.nix's serviceName/generated.json lookup: finix
      # guests are a separate finixSystem module-tree evaluation from
      # nixos guests' nixosSystem one, so renderer.nix's option
      # declaration and generated.json read aren't visible here -- there's
      # no shared option namespace across the two, just the same on-disk
      # generated.json both trees happen to read.
      generated = builtins.fromJSON (builtins.readFile ../generated.json);
      svcName = config.microCompose.serviceName;
      gen = generated.services.${svcName}
        or (throw "microbe: no generated data for service '${svcName}'");

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
        # finix's own qemu.nix appends config.virtualisation.qemu.extraArgs
        # to the end of its built argv, so this is a pure addition --
        # finix-base.nix's dropVirtfsArgs/explicitShareArgs argv rewrite
        # (which only touches -virtfs pairs) passes it through untouched.
        virtualisation.qemu.extraArgs = [
          "-device"
          "vhost-vsock-pci,id=vsock0,guest-cid=${toString gen.cid}"
        ];

        # The vhost-vsock-pci device on the QEMU command line above gives
        # the guest kernel a PCI device, but CONFIG_VIRTIO_VSOCKETS is
        # built as a module (vmw_vsock_virtio_transport.ko), not built in,
        # and nothing auto-probes it: finix's modprobe.nix only generates a
        # modprobe.<mod> task per entry in boot.kernelModules (verified live
        # -- boot.kernelModules = ["fuse" "9p" "loop" "atkbd"] on this guest
        # produced exactly those four modprobe.* finit tasks and no others,
        # so PF_VSOCK registered from the always-built core but the PCI
        # transport driver never loaded and microbe-agent's AF_VSOCK listen
        # silently never came up -- `ss --vsock -a` on the host showed no
        # listener and DialVsock timed out after a real boot+connect test).
        boot.kernelModules = [ "vmw_vsock_virtio_transport" ];

        # finit's analogue of agent.nix's systemd unit: respawn = true is
        # Restart=always; restart_sec is RestartSec. finit has no
        # unit-dependency graph (no after = network.target equivalent) --
        # runlevels alone (default "234") is enough ordering here.
        finit.services.microbe-agent = {
          command = "${agentPkg}/bin/microbe-agent";
          respawn = true;
          restart_sec = 1;
        };
      };
    };
}
