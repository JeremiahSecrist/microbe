# finix-base.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.finix-base (Dendritic pattern, same
# mechanism as guest-base.nix/virtiofsd-run.nix), pulled in by finix
# ServicePart's modules list (see flake.go's finix branch of ServicePart)
# alongside a direct-path import of finix's own virtualisation/qemu.nix --
# qemu.nix isn't exposed under finix's own self.nixosModules.* (only
# ./programs and ./services subdirs get auto-registered there, see finix's
# modules/default.nix), so ServicePart imports it by path from the finix
# flake input instead of by name.
#
# finix's qemu.nix already handles the boot chain (kernel/initrd/init=,
# see its bootModeArgs.kernel) and the host /nix/store share
# (mountHostNixStore defaults to true for bootMode = "kernel", the only
# mode it supports) -- this module only needs to add console output and
# wrap virtualisation.qemu.argv (a plain list of strings, not a buildable
# derivation on finix's side, unlike microvm.nix's declaredRunner) into an
# actual runnable derivation so microbe's build-target attrpath
# (".config.microbe.qemuRunner", see stack.go's buildTarget) has something
# to build.
{
  flake.nixosModules.finix-base = { config, lib, pkgs, ... }:
    let
      # qemu.nix emits `-virtfs local,...,mount_tag=<tag>,...` per shared
      # directory, but under this qemu/machine-type combination that
      # shorthand doesn't reliably attach a virtio-9p PCI device -- booted
      # by hand, the guest reports "9pnet_virtio: no channels available"
      # for the tag and can't mount /nix/store (neededForBoot), so it never
      # reaches switch-root. The explicit `-fsdev` + `-device
      # virtio-9p-pci` pair for the same share works (verified by hand:
      # store mounts, boot proceeds past that point). This rewrites argv to
      # drop every `-virtfs <spec>` pair and append the explicit
      # equivalent for each of config.virtualisation.qemu.sharedDirectories
      # instead, leaving the rest of qemu.nix's argv (kernel/initrd/init=,
      # -m, -smp, -nic, ...) untouched.
      dropVirtfsArgs = args:
        if args == [ ] then [ ]
        else if builtins.head args == "-virtfs"
        then dropVirtfsArgs (builtins.tail (builtins.tail args))
        else [ (builtins.head args) ] ++ dropVirtfsArgs (builtins.tail args);

      explicitShareArgs = lib.flatten (lib.mapAttrsToList (tag: share: [
        "-fsdev"
        "local,id=${tag},path=${share.source},security_model=${share.securityModel},readonly=on"
        "-device"
        "virtio-9p-pci,fsdev=${tag},mount_tag=${tag}"
      ]) config.virtualisation.qemu.sharedDirectories);
    in
    {
      options.microbe.qemuRunner = lib.mkOption {
        type = lib.types.package;
        description = "A script that execs the QEMU invocation for this finix guest.";
      };

      config = {
        boot.kernelParams = [ "console=ttyS0" ];

        microbe.qemuRunner = pkgs.writeShellScriptBin "run-vm" ''
          exec ${lib.concatMapStringsSep " " lib.escapeShellArg
            (dropVirtfsArgs config.virtualisation.qemu.argv ++ explicitShareArgs)} "$@"
        '';
      };
    };
}
