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

        # finix has no default root filesystem -- unlike NixOS, nothing
        # forces fileSystems."/" to exist. Without it, no fs ever has
        # mountPoint "/", so filesystems/options.nix's neededForBoot-forcing
        # (pathsNeededForBoot includes "/") never fires, /sysroot is never
        # mounted, and stage-1's switch-root task (finit/initrd.nix) silently
        # takes its "not a mountpoint" failure branch -- printing "rescue
        # shell is disabled / rebooting in 10s" and rebooting, with no
        # further diagnostic. finix's own test harness
        # (tests/lib/default.nix) sets exactly this tmpfs root for every VM
        # it boots; mirror it here.
        fileSystems."/" = {
          device = "tmpfs";
          fsType = "tmpfs";
          options = [ "mode=755" ];
        };

        # The virtio-9p PCI device for the nix-store share is intermittently
        # not yet bound by virtio_pci when finit's stage-1 mount task runs
        # (a widely-reported, pre-existing kernel/qemu race between PCI
        # driver probing and the 9p mount attempt -- see e.g.
        # https://forum.proxmox.com/threads/debian-guest-virtio-9p-no-channels-available.30525/
        # and the LKML thread "9p: Fix probe failed when modprobe
        # 9pnet_virtio" -- not something introduced by finix-base.nix's own
        # -fsdev/-device rewrite above, which is otherwise correct and was
        # verified working). Observed live: roughly half of boots hit
        # "9pnet_virtio: no channels available for device nix-store" on the
        # first mount attempt and never recover, since finit/mount.nix's
        # generated task has no retry. Override just this one task's command
        # with a short retry loop -- mirrors the same polling pattern
        # finit/mount.nix itself already uses for its own wait-dev tasks --
        # instead of failing immediately. Set via `.script` (not `.command`
        # directly): scriptOpts (finit/stage1.nix) is what makes finit copy
        # the generated script derivation into the initrd's closed content
        # set (initrd.nix's mkScriptFile) -- a bare `.command` pointing at a
        # derivation is never added to /etc/finit.d's referenced store paths
        # and fails at finit startup with "skipping ...: No such file or
        # directory" (verified live).
        boot.initrd.finit.tasks."mount-nix-.ro-store".script = ''
          tries=50
          i=0
          while [ "$i" -lt "$tries" ]; do
            if mount -t 9p -o trans=virtio,version=9p2000.L,cache=loose,X-mount.mkdir nix-store /sysroot/nix/.ro-store; then
              exit 0
            fi
            i=$((i + 1))
            sleep 0.2
          done
          printf 'mount-nix-.ro-store: giving up after %s tries\n' "$tries" >&2
          exit 1
        '';

        microbe.qemuRunner = pkgs.writeShellScriptBin "run-vm" ''
          exec ${lib.concatMapStringsSep " " lib.escapeShellArg
            (dropVirtfsArgs config.virtualisation.qemu.argv ++ explicitShareArgs)} "$@"
        '';
      };
    };
}
