# finix-base.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.finix-base (Dendritic pattern, same
# mechanism as guest-base.nix/virtiofsd-run.nix), pulled in by finix
# ServicePart's modules list (see flake.go's finix branch of ServicePart).
#
# Drives cloud-hypervisor directly instead of QEMU (finix's own
# modules/virtualisation/qemu.nix is no longer imported at all) so finix
# guests use the same hypervisor as nixos guests -- finix has no
# cloud-hypervisor support upstream, and microvm.nix's own NixOS module
# can't be imported into a finix guest tree (it assumes systemd; finix runs
# finit), so this hand-builds the cloud-hypervisor invocation, modeled on
# microvm.nix's real lib/runners/cloud-hypervisor.nix, against finix's own
# generic boot/filesystems option trees (config.boot.kernelPackages,
# config.boot.initrd.package, fileSystems, config.microbe.netInterfaces from
# finix-network.nix) -- none of which depend on qemu.nix itself.
#
# cloud-hypervisor only supports virtiofs for `--fs` (not 9p), so unlike the
# old QEMU path, /nix/store is shared via virtiofs (matching the live-share
# design docs/finix-microvm-plan.md already committed to) -- unlike nixos
# guests, which default to a baked erofs store-disk image instead (see
# microvm.nix's `storeOnDisk` default: true unless a share named exactly
# `/nix/store` exists, which renderer.nix never adds). finix intentionally
# keeps the live-share design, so it always needs virtiofsd for the store,
# even with zero user-declared share volumes (see internal/cmd/lifecycle.go's
# hasVirtiofsShare, made OS-aware for exactly this).
{
  flake.nixosModules.finix-base = { config, lib, pkgs, ... }:
    let
      compose = import ../microbe.nix;
      generated = builtins.fromJSON (builtins.readFile ../generated.json);

      svcName = config.microCompose.serviceName;
      svc = compose.services.${svcName}
        or (throw "microbe: no service '${svcName}' in compose file");
      gen = generated.services.${svcName}
        or (throw "microbe: no generated data for service '${svcName}'");

      vcpu = svc.vcpu or 1;
      mem = svc.mem or 512;

      # "share" is the default volume type when omitted (see config.load's
      # applyDefaults) -- mirrored here since this module imports the user's
      # raw microbe.nix directly and doesn't go through Go's defaulting.
      volumeType = v: v.type or "share";

      # User-declared share volumes -- disk-type volumes aren't wired for
      # finix yet (cloud-hypervisor `--disk` + guest-side mount is a
      # separate, unimplemented piece; only share/virtiofs is in scope
      # here). Owner-uid translation (renderer.nix's `--translate-uid`) is
      # also not yet ported -- shares mount with virtiofsd's own uid, no
      # guest-user remapping.
      userShares = lib.optionals (svc ? volumes) (map (v: {
        tag = v.name;
        source = gen.volumes.${v.name}.host
          or (throw "microbe: service '${svcName}': share '${v.name}' needs a host path");
        mountPoint = v.target;
        readOnly = (v.mode or "rw") == "ro";
      }) (builtins.filter (v: volumeType v == "share") svc.volumes or [ ]));

      # /nix/store is always shared, mandatory, read-only -- see module
      # comment above for why finix diverges from nixos's default here.
      allShares = [{
        tag = "nix-store";
        source = builtins.storeDir;
        mountPoint = "/nix/.ro-store";
        readOnly = true;
      }] ++ userShares;

      # <rundir>/<svcName>-virtiofs-<tag>.sock, relative to the runner's
      # CWD -- matches internal/cmd/lifecycle.go's virtiofsShareSockets
      # naming exactly (svcName, not networking.hostName, since that's the
      # convention up.go's waitForSocket already waits on).
      virtiofsSocket = tag: "${svcName}-virtiofs-${tag}.sock";

      # cid/socket path convention matches nixos's own hybrid-vsock scheme
      # (internal/cmd/agentsession.go's resolveGuestVsock/dialAgent) exactly
      # -- cloud-hypervisor's --vsock is the same hybrid UDS device
      # regardless of guest OS, so finix needs no special-cased dial path.
      vsockPath = "notify.vsock";

      # Mounts a virtiofs share by tag, waiting for the device to actually
      # be ready with no `sleep`-based polling anywhere:
      #  1. try the mount immediately (fast path -- the device may already
      #     be up).
      #  2. if that fails, block on a read of /dev/kmsg (opened *before*
      #     the first attempt, so a tag discovered in the gap isn't missed
      #     -- /dev/kmsg only replays messages written after open) for the
      #     kernel's own "discovered new tag: <tag>" line. This wakes up
      #     exactly when virtio_pci finishes probing the device, no busy-
      #     wait, no arbitrary poll interval. `timeout` bounds the wait so
      #     boot fails loudly instead of hanging forever if the device
      #     never shows.
      #  3. even after that kmsg line prints, the mount can still fail a
      #     few more times -- verified live: the printk lands a hair before
      #     the tag is actually usable by mount(2), a genuine sub-
      #     millisecond kernel-internal race, not something a single retry
      #     reliably wins. A short, sleep-free, iteration-bounded tight
      #     loop (not time-bounded) closes that window without
      #     reintroducing an arbitrary delay.
      mountVirtiofs = tag: mountPoint: ''
        exec 3</dev/kmsg
        if ! mount -t virtiofs -o X-mount.mkdir ${lib.escapeShellArg tag} ${lib.escapeShellArg mountPoint}; then
          timeout 20 sh -c 'while read -r line; do case "$line" in *"discovered new tag: ${tag}"*) exit 0 ;; esac; done' <&3
          exec 3<&-
          tries=0
          while [ "$tries" -lt 1000 ]; do
            mount -t virtiofs -o X-mount.mkdir ${lib.escapeShellArg tag} ${lib.escapeShellArg mountPoint} && break
            tries=$((tries + 1))
          done
          if [ "$tries" -ge 1000 ]; then
            printf 'mountVirtiofs ${tag}: mount still failing after tag discovery\n' >&2
            exit 1
          fi
        fi
      '';

      # init=<toplevel>/init: previously supplied by finix's own
      # virtualisation/qemu.nix (no longer imported -- see module comment
      # above), which set this from config.system.topLevel unconditionally.
      # Without it, stage-1's switch-root script (modules/finit/initrd.nix)
      # falls back to its "/init" default, which nothing ever creates in
      # /sysroot (a bare tmpfs), so `[ ! -x "/sysroot$stage2Init" ]` is
      # always true and switch-root always takes its "rescue shell is
      # disabled, rebooting in 10s" failure path -- verified live: boot
      # reaches switch-root and reboots in a loop with this omitted, and
      # proceeds past it once init= is added back.
      kernelCmdLine = "earlyprintk=ttyS0 console=ttyS0 reboot=t panic=-1 init=${config.system.topLevel}/init "
        + toString config.boot.kernelParams;

      netArgs = map (n: "tap=${n.id},mac=${n.mac}") config.microbe.netInterfaces;

      argv = [
        "${pkgs.cloud-hypervisor}/bin/cloud-hypervisor"
        "--cpus" "boot=${toString vcpu}"
        "--kernel" "${config.boot.kernelPackages.kernel.dev}/vmlinux"
        "--initramfs" "${config.boot.initrd.package}/initrd"
        "--cmdline" kernelCmdLine
        "--seccomp" "true"
        "--watchdog"
        # shared=on is mandatory whenever any virtiofs share exists (finix
        # always has the store share) -- and mergeable cannot be set at the
        # same time (cloud-hypervisor rejects shared+mergeable together),
        # see the real lib/runners/cloud-hypervisor.nix's memOps.
        "--memory" "size=${toString mem}M,shared=on"
        "--console" "null"
        "--serial" "tty"
        "--vsock" "cid=${toString gen.cid},socket=${vsockPath}"
        "--api-socket" "${svcName}.sock"
      ]
      ++ lib.concatMap (s: [ "--fs" "tag=${s.tag},socket=${virtiofsSocket s.tag}" ]) allShares
      ++ lib.concatMap (n: [ "--net" n ]) netArgs;
    in
    {
      options.microbe.qemuRunner = lib.mkOption {
        type = lib.types.package;
        description = "A script that execs the cloud-hypervisor invocation for this finix guest.";
      };

      options.microbe.extraRunnerBins = lib.mkOption {
        type = lib.types.listOf lib.types.package;
        default = [ ];
        description = ''
          Extra bin/* packages joined into microbe.qemuRunner alongside
          bin/microvm-run -- e.g. finix-virtiofsd-run.nix's bin/virtiofsd-run,
          which internal/runtime.StartVirtiofsd looks up in the same runner
          output runtime.StartService already resolves bin/microvm-run from.
        '';
      };

      # finix has a filesystems/<fs>.nix module (declaring
      # boot.initrd.supportedFilesystems.<fs>.enable, e.g. btrfs.nix/
      # ext4.nix/tmpfs.nix) for every fstype it knows about, and
      # filesystems/default.nix force-sets .enable = true for whatever
      # fstype any neededForBoot fileSystems entry declares -- erroring
      # ("option does not exist") if no such module exists. finix has no
      # filesystems/virtiofs.nix (confirmed: grepped its modules tree, only
      # btrfs/ext2/ext4/f2fs/vfat/xfs/zfs/luks/lvm/ntfs3/tmpfs/9p/none/
      # fuse.mergerfs exist), so the store's neededForBoot virtiofs mount
      # below needs this option declared here instead. The `enable` value
      # itself is inert -- boot.initrd.kernelModules already force-loads
      # "virtiofs" unconditionally below, independent of this flag; this
      # option exists purely to satisfy default.nix's auto-registration.
      options.boot.initrd.supportedFilesystems.virtiofs.enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
      };
      # filesystems/default.nix's non-initrd variant of the same
      # auto-registration, triggered by every fileSystems entry (not just
      # neededForBoot ones) -- user share mounts hit this one too.
      options.boot.supportedFilesystems.virtiofs.enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
      };

      options.microbe.netInterfaces = lib.mkOption {
        type = lib.types.listOf (lib.types.submodule {
          options = {
            id = lib.mkOption { type = lib.types.str; description = "Host tap device name."; };
            mac = lib.mkOption { type = lib.types.str; description = "Guest NIC MAC address."; };
          };
        });
        default = [ ];
        description = "Tap interfaces to attach, populated by finix-network.nix.";
      };

      options.microbe.virtiofsShares = lib.mkOption {
        type = lib.types.listOf (lib.types.submodule {
          options = {
            tag = lib.mkOption { type = lib.types.str; };
            source = lib.mkOption { type = lib.types.str; };
            readOnly = lib.mkOption { type = lib.types.bool; default = false; };
          };
        });
        default = [ ];
        description = ''
          virtiofs shares this guest needs virtiofsd instances for --
          always includes the mandatory /nix/store share plus any
          user-declared share volumes. Consumed by finix-virtiofsd-run.nix
          to build the matching bin/virtiofsd-run companion script.
        '';
      };

      config = {
        # virtiofsShares only declares tag/source/readOnly -- mountPoint is
        # only needed locally for the fileSystems entries above.
        microbe.virtiofsShares = map (s: { inherit (s) tag source readOnly; }) allShares;

        # finix has no default root filesystem -- unlike NixOS, nothing
        # forces fileSystems."/" to exist. Without it, no fs ever has
        # mountpoint "/", so filesystems/options.nix's neededForBoot-forcing
        # never fires, /sysroot is never mounted, and stage-1's switch-root
        # task silently reboots with no diagnostic. finix's own test harness
        # (tests/lib/default.nix) sets exactly this tmpfs root for every VM
        # it boots; mirror it here.
        #
        # The rest mirrors finix's own qemu.nix config block (no longer
        # imported): a plain virtiofs fileSystems entry per share,
        # neededForBoot on the store since switch-root needs it, plus the
        # store's own /nix/store -> /nix/.ro-store bind (finix-setup
        # remounts this read-write later, same as under the old QEMU path).
        # All entries combine via mkMerge into one `fileSystems` value --
        # dotted-attr assignment (fileSystems."/" = ...) and a separate
        # `fileSystems = mkMerge [...]` in the same attrset would collide.
        fileSystems = lib.mkMerge ([
          {
            "/" = {
              device = "tmpfs";
              fsType = "tmpfs";
              options = [ "mode=755" ];
            };
            "/nix/.ro-store" = {
              device = "nix-store";
              fsType = "virtiofs";
              neededForBoot = true;
            };
            "/nix/store" = {
              device = "/nix/.ro-store";
              fsType = "none";
              options = [ "bind" ];
              neededForBoot = true;
            };
          }
        ] ++ map (s: {
          ${s.mountPoint} = {
            device = s.tag;
            fsType = "virtiofs";
          };
        }) userShares);

        # User-declared share volumes are deliberately not neededForBoot
        # (only the store needs to be available before switch-root), so
        # finix's stage-1 mount.nix (boot.initrd.finit.tasks, see above)
        # never touches them -- and unlike NixOS/systemd, finix has no
        # stage-2 equivalent that auto-mounts ordinary `fileSystems`
        # entries after switch-root either (grepped modules/finit/stage2.nix:
        # no fileSystems handling at all). Without an explicit task here,
        # a declared share volume would stay declared-but-never-mounted
        # forever. One retry-loop finit *service* per share, regular
        # (post-switch-root) `finit.services` namespace -- same convention
        # finix-agent.nix already uses for microbe-agent -- mirroring the
        # store mount's retry pattern for the same PCI-probe-race reason.
        # stage-2's task submodule (unlike stage-1's, see boot.initrd.finit
        # above) has no `.script` convenience mixin -- only `.command`,
        # which takes a program (a store path), same as scriptOpts itself
        # builds under the hood -- so build the script with
        # pkgs.writeShellScript directly.
        finit.tasks = builtins.listToAttrs (map (s: {
          name = "mount-share-${s.tag}";
          value = {
            command = pkgs.writeShellScript "mount-share-${s.tag}" (mountVirtiofs s.tag s.mountPoint);
          };
        }) userShares);

        # "virtio_blk" used to be listed here too, loading the module even
        # though this guest never gets a --disk device (only --fs shares,
        # see allShares/argv above) -- confirmed live via /proc/modules on
        # a booted guest that it was the only reason virtio_blk ever
        # loaded (nothing else requests it). Dropped.
        boot.initrd.kernelModules = [
          "virtio_pci" # TODO: platforms without PCI.
          "virtio_console"
          "virtio_net"
          "virtiofs"
        ];

        # Trim the guest kernel's build config for a cloud-hypervisor-only
        # VM guest with no physical hardware and no disk-type volumes (see
        # the userShares comment above -- disk volumes aren't wired for
        # finix yet, so root stays tmpfs and everything else is virtiofs).
        # Uses boot.kernelPatches (not an override of boot.kernelPackages
        # directly) because finix's own modules/boot/kernel.nix already
        # merges config.boot.kernelPatches into config.boot.kernelPackages
        # via its `apply` function -- see that module's `kernelPatches =
        # (originalArgs.kernelPatches or [ ]) ++ config.boot.kernelPatches`.
        # `patch = null` (no actual source patch, config-only) is the
        # documented pattern for a structuredExtraConfig-only kernelPatches
        # entry. Nixpkgs's generic kernel builder validates the resulting
        # .config against what was requested and fails the *build* loudly
        # on any option it couldn't actually satisfy (e.g. a Kconfig
        # `select` conflict) -- so a bad entry here is caught before any
        # boot attempt, not a silent runtime risk. Deliberately not setting
        # `ignoreConfigErrors` anywhere, which would suppress that check.
        boot.kernelPatches = [{
          name = "microbe-finix-guest-trim";
          patch = null;
          structuredExtraConfig = with lib.kernel; {
            # Filesystem drivers this guest never mounts -- root is tmpfs,
            # /nix/store and share volumes are virtiofs (FUSE_FS/VIRTIO_FS
            # deliberately left untouched, virtiofs depends on FUSE_FS), and
            # no disk-type volumes exist for finix yet, so none of the
            # on-disk or network filesystem drivers below are reachable.
            # Kept to the subset nixpkgs' generic kernel builder actually
            # accepts cleanly (see the long list of things *not* trimmed
            # below, and why, for everything that didn't fit that bar).
            XFS_FS = no;
            JFS_FS = no;
            HFS_FS = no;
            HFSPLUS_FS = no;
            "9P_FS" = no; # superseded by the virtiofs path this port added.
            AFS_FS = no;
            OVERLAY_FS = no;

            # One piece of physical hardware this VM guest never has: no
            # PS/2 keyboard controller (ttyS0 is the only input/output path
            # -- see the "There is no boot hang" note below -- and ATKBD's
            # on-demand modprobe was one of the four concurrent module
            # loads implicated in the ~1s runlevel-2 stall
            # docs/finix-microvm-plan.md's boot-speed investigation traced
            # but didn't fix; removing the driver entirely means that
            # modprobe now fails fast instead of racing).
            KEYBOARD_ATKBD = no;

            # Confirmed via /proc/modules on a booted guest (real ground
            # truth, not a guess): dm_mod and loop both load by default but
            # neither is ever actually used -- no LVM/device-mapper target
            # and no loop device is ever created anywhere in this guest's
            # boot path (root is tmpfs, everything else is a direct
            # virtiofs mount, see mountVirtiofs above).
            BLK_DEV_DM = no;
            BLK_DEV_LOOP = no;

            # --- Everything below was tried and deliberately reverted ---
            #
            # nixpkgs' generic kernel builder validates the resulting
            # .config against what was requested and fails the *build*
            # loudly (never reaching a boot attempt) whenever an option it
            # was explicitly told to enable becomes unreachable, or when a
            # requested value collides with one nixpkgs already sets at
            # equal priority. Every option below hit one of those two
            # failure modes, all caught at build time:
            #
            # EXT4_FS, EXT2_FS, BTRFS_FS, CIFS, CEPH_FS: common-config.nix
            # sets each of these filesystems' xattr/posix_acl/security
            # suboptions with a bare `= yes;` (not the softer `option yes`
            # helper most other filesystems use), so disabling the parent
            # makes those suboptions unreachable -- a hard error, not a
            # dependency the module system can quietly resolve away.
            #
            # F2FS_FS, UDF_FS, NFS_FS, NFSD, ISO9660_FS: common-config.nix
            # sets these filesystems (or, for NFSD, its ACL/V4 suboptions)
            # directly at normal (non-default) module-system priority --
            # `no` collided outright ("conflicting definition values")
            # rather than cleanly overriding.
            #
            # NTFS3_FS: Kconfig itself (not the Nix module system) forces
            # it on -- the deprecated read-only NTFS_FS driver `select`s
            # NTFS3_FS whenever NTFS_FS is enabled, so "n" isn't even a
            # legal answer for it (kernel config generation kept
            # re-asking and the build died on EOF).
            #
            # VFAT_FS, MSDOS_FS, FAT_FS: disabling all three still left
            # FAT_FS itself flagged as an unreachable explicitly-requested
            # option elsewhere in the generic x86 config (not traced to a
            # specific source file -- not worth the further digging for a
            # driver this small).
            #
            # SOUND: common-config.nix explicitly enables a long list of
            # Intel SOF sound-DSP submodules (SND_SOC_SOF_*) at normal
            # priority; disabling SOUND makes every one of those
            # unreachable. Fixing it would mean overriding each submodule
            # individually too -- well past "easy win" for a driver that,
            # being a module, was never going to load anyway without
            # matching hardware.
            #
            # WLAN, CFG80211, MAC80211, BT: same "explicit enable becomes
            # unreachable" problem, this time across a long tail of
            # individual wifi/bluetooth radio and HID drivers
            # (RTW88/RTW88_8822BE/RT2800USB_RT53XX/NVIDIA_SHIELD_FF/etc.).
            # None of them are ever reachable at runtime on this guest (no
            # matching hardware exists to probe for), so the cost of
            # leaving them buildable is build-time-only, and fighting each
            # cascade individually isn't worth it here.
            #
            # DRM, FB, USB_SUPPORT: common-config.nix sets DRM=y at normal
            # (non-default) priority; `no` collided outright the same way
            # the filesystem group above did. Left alone rather than
            # reaching for lib.mkForce to fight a value nixpkgs evidently
            # wants non-negotiable for its generic kernel build.
          };
        }];

        # The virtio-fs PCI device for the nix-store share is intermittently
        # not yet bound by virtio_pci when finit's auto-generated stage-1
        # mount task ("mount-nix-.ro-store", from the neededForBoot
        # fileSystems entry above) runs -- the same class of race the old
        # 9p path hit (kernel/PCI-probe vs. mount-attempt ordering isn't
        # guaranteed), just with a different symptom: "virtio-fs: tag
        # <nix-store> not found" instead of 9p's "no channels available"
        # (verified live: the device's own "discovered new tag: nix-store"
        # kernel log line lands *after* the failed mount attempt, and the
        # auto-generated task has no retry, so boot silently hangs forever
        # with no reboot and no diagnostic beyond the one kernel line).
        #
        # Override just this one task to wait on the kernel's own tag-
        # discovery event (see mountVirtiofs above) instead of finit's
        # single unretried attempt. Critically, unlike finix's own
        # auto-generated command (mount.nix's `mount -o ${opts}`, where
        # opts always includes "X-mount.mkdir"), our hand-written mount
        # command must pass -o X-mount.mkdir itself -- verified live the
        # hard way: without it, /sysroot/nix/.ro-store never gets created,
        # so `mount -t virtiofs` always fails (mount point does not exist,
        # not the PCI race this override exists for), the task never
        # succeeds, and everything downstream (the /nix/store bind mount,
        # switch-root's init= existence check) fails in turn -- easy to
        # misdiagnose as a finit condition/scheduling bug from the console
        # alone, since finit's boot-progress UI prints "[ OK ]" for a task
        # as soon as it *starts*, not when it completes, which makes a task
        # that's silently failing in the background look identical to one
        # that already succeeded.
        boot.initrd.finit.tasks."mount-nix-.ro-store".script =
          mountVirtiofs "nix-store" "/sysroot/nix/.ro-store";

        # There is no boot hang: runlevel 2 completes in ~5s regardless of
        # finit version. `services.getty` only spawns on tty1-6 (virtual
        # consoles) by default -- invisible under cloud-hypervisor's
        # `--serial tty`, and disjoint from the kernel cmdline's
        # `console=ttyS0` above (that only routes kernel/finit log output,
        # it doesn't add a getty). Set ttyS0 as the *only* getty target --
        # not the default tty1-6 virtual consoles, which are invisible
        # under cloud-hypervisor's `--serial tty` and never watched --
        # shaving 6 unnecessary "Starting tty:N"/"Starting getty" task
        # spawns off every boot (verified live under QEMU originally with
        # the fuller tty1-6+ttyS0 list; trimmed here since only ttyS0 was
        # ever actually used).
        services.getty.ttys = [ "ttyS0" ];

        # symlinkJoin, not a bare writeShellScriptBin, so finix-virtiofsd-
        # run.nix's bin/virtiofsd-run can land in the same runner output
        # directory as bin/microvm-run -- runtime.StartService/
        # StartVirtiofsd both resolve their binName relative to whatever
        # single store path microbe.qemuRunner's build target points at.
        microbe.qemuRunner = pkgs.symlinkJoin {
          name = "${svcName}-runner";
          paths = [
            (pkgs.writeShellScriptBin "microvm-run" ''
              exec ${lib.concatMapStringsSep " " lib.escapeShellArg argv} "$@"
            '')
          ] ++ config.microbe.extraRunnerBins;
        };
      };
    };
}
