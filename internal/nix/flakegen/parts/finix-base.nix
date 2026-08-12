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
#
# Service-specific values (svcName, vcpu, mem, cid, userShares) are consumed
# from config.microbe.finix.* options rather than imported directly from
# microbe.nix/generated.json. In generated service stacks, finix-compose.nix
# populates those options by reading the compose/generated files. In the main
# flake's standalone test VM (nix run .#microvm-finix), nix/finix-test-config.nix
# sets them directly with hardcoded values, requiring no compose/generated files.
{
  flake.nixosModules.finix-base = { config, lib, pkgs, ... }:
    let
      inherit (config.microbe.finix) svcName vcpu mem cid userShares;

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
      kernelCmdLine = "console=ttyS0 quiet loglevel=3 reboot=t panic=-1 "
        + "8250.nr_uarts=1 cryptomgr.notests=1 no_timer_check mitigations=off "
        + "random.trust_cpu=on "
        + "init=${config.system.topLevel}/init "
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
        "--vsock" "cid=${toString cid},socket=${vsockPath}"
        "--api-socket" "${svcName}.sock"
      ]
      ++ lib.concatMap (s: [ "--fs" "tag=${s.tag},socket=${virtiofsSocket s.tag}" ]) allShares
      ++ lib.concatMap (n: [ "--net" n ]) netArgs;
    in
    {
      options.microbe.finix = {
        svcName = lib.mkOption {
          type = lib.types.str;
          description = "Service name, used for runner/socket naming conventions.";
        };
        vcpu = lib.mkOption {
          type = lib.types.int;
          default = 1;
          description = "Number of vCPUs to give the guest.";
        };
        mem = lib.mkOption {
          type = lib.types.int;
          default = 512;
          description = "Memory in MiB to give the guest.";
        };
        cid = lib.mkOption {
          type = lib.types.int;
          description = "AF_VSOCK context ID for this guest (must be unique per host).";
        };
        userShares = lib.mkOption {
          type = lib.types.listOf (lib.types.submodule {
            options = {
              tag = lib.mkOption { type = lib.types.str; description = "virtiofs tag (socket name suffix)."; };
              source = lib.mkOption { type = lib.types.str; description = "Host-side directory to share."; };
              mountPoint = lib.mkOption { type = lib.types.str; description = "Guest-side mount point."; };
              readOnly = lib.mkOption { type = lib.types.bool; default = false; };
            };
          });
          default = [ ];
          description = "User-declared share volumes (populated by finix-compose.nix in service stacks).";
        };
      };

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
        # All virtio drivers are built-in (=y) in the microbe kernel config --
        # listing them here generates unnecessary modprobe calls in stage-1.
        boot.initrd.kernelModules = lib.mkForce [];

        # finix's default availableKernelModules includes physical-hardware
        # drivers (uhci_hcd, ata_piix, sd_mod, etc.) that don't exist in the
        # trimmed microVM kernel. modules-shrunk hard-errors if any listed
        # module is absent. Clear the list -- all virtio drivers are built-in
        # (=y) so they don't need to be in availableKernelModules.
        boot.initrd.availableKernelModules = lib.mkForce [ ];

        # finix's default kernel.nix adds ["loop" "atkbd"] to
        # boot.kernelModules, which finit's modules-load plugin reads and
        # creates per-module modprobe tasks for. Since loop and atkbd are
        # built-in (=y) in our kernel config, clear the list to avoid
        # unnecessary modprobe tasks that cause a ~1s race stall.
        boot.kernelModules = lib.mkForce [ ];

        # All drivers in the microVM kernel are built-in (=y) — no module
        # loading is ever needed. Disable finix's modprobe module entirely:
        # it creates a finit batch task, writes /proc/sys/kernel/modprobe,
        # and installs kmod — none of which we need. The finit task was
        # also racing on cond_set_oneshot_noupdate() (~1s stall).
        boot.modprobeConfig.enable = false;

        # Kernel trimming has moved to parts/microbe-kernel.nix, which
        # applies to both finix and NixOS guests and is imported by
        # ServicePart for both OS types. The config in microbe-kernel.nix
        # supersedes the boot.kernelPatches block that previously lived
        # here -- it sets boot.kernelPackages directly (overriding the base
        # kernel entirely) rather than patching it, which means finix's own
        # modules/boot/kernel.nix `apply` function still sees a valid
        # kernelPackages value and does not need boot.kernelPatches at all.
        # The constraint landscape (what's safe to trim vs. what conflicts
        # with nixpkgs common-config.nix) is documented in nix/microbe-kernel.nix.

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
        microbe.qemuRunner =
          let
            # Snapshot is keyed to the guest system derivation so it
            # auto-invalidates whenever the NixOS config changes.
            snapKey = builtins.substring 11 32 config.system.topLevel;
            chRemote = "${pkgs.cloud-hypervisor}/bin/ch-remote";
            chBin    = "${pkgs.cloud-hypervisor}/bin/cloud-hypervisor";
          in
          pkgs.symlinkJoin {
            name = "${svcName}-runner";
            paths = [
              (pkgs.writeShellScriptBin "microvm-run" ''
                # Probe microbe-agent on hybrid-vsock port 6969.  Succeeds once
                # finit has fully booted and the agent is accepting connections.
                probe_agent() {
                  printf 'CONNECT 6969\n' \
                    | ${lib.escapeShellArg "${pkgs.socat}/bin/socat"} \
                        -t1 UNIX-CONNECT:${lib.escapeShellArg vsockPath},connect-timeout=1 - \
                        2>/dev/null \
                    | grep -q "^OK"
                }

                # Snapshot lives alongside the run dir (CWD = stable runDir set
                # by microbe's runtime.StartService).  Keyed to the system
                # derivation hash so it auto-invalidates on config changes.
                SNAP_DIR="snapshots/${snapKey}"

                if [ -d "$SNAP_DIR" ]; then
                  # ── RESTORE PATH (subsequent boots) ─────────────────────────
                  # virtiofsd-run is already up; cloud-hypervisor reads device
                  # config (memory, CPUs, --fs sockets, --net taps) from the
                  # snapshot and reconnects to virtiofsd at the stored path.
                  # --serial/--console/--kernel must NOT be passed with --restore:
                  # CH v53 hangs when --kernel is combined with --restore, and
                  # --serial/--console alone require --kernel. VM runs headlessly;
                  # access via `microbe exec` (vsock) rather than serial console.
                  exec ${lib.escapeShellArg chBin} \
                    --api-socket ${lib.escapeShellArg "${svcName}.sock"} \
                    --restore "source_url=file://$(pwd)/$SNAP_DIR,resume=true" \
                    "$@"
                else
                  # ── FIRST-BOOT PATH ──────────────────────────────────────────
                  # Boot normally, wait for agent-ready, then snapshot so the
                  # next start can restore in <100 ms instead of booting fresh.
                  ${lib.concatMapStringsSep " \\\n  " lib.escapeShellArg argv} "$@" &
                  CH_PID=$!
                  cleanup() {
                    kill "$CH_PID" 2>/dev/null
                    wait "$CH_PID" 2>/dev/null
                    rm -rf "$SNAP_DIR.tmp"
                  }
                  trap cleanup EXIT INT TERM

                  # Wait for cloud-hypervisor API socket to appear.
                  while [ ! -S ${lib.escapeShellArg "${svcName}.sock"} ]; do sleep 0.05; done

                  # Wait for finit to finish booting (agent ready on vsock 6969).
                  until probe_agent; do sleep 0.1; done

                  # Pause → snapshot → resume.  Write to a .tmp dir first so a
                  # failed snapshot never leaves a partial result to restore from.
                  mkdir -p "$SNAP_DIR.tmp"
                  ${lib.escapeShellArg chRemote} \
                    --api-socket ${lib.escapeShellArg "${svcName}.sock"} pause
                  ${lib.escapeShellArg chRemote} \
                    --api-socket ${lib.escapeShellArg "${svcName}.sock"} \
                    snapshot "file://$(pwd)/$SNAP_DIR.tmp"
                  mv "$SNAP_DIR.tmp" "$SNAP_DIR"
                  ${lib.escapeShellArg chRemote} \
                    --api-socket ${lib.escapeShellArg "${svcName}.sock"} resume

                  # Disarm the rm-on-exit cleanup; keep the kill-on-exit part.
                  trap 'kill "$CH_PID" 2>/dev/null; wait "$CH_PID" 2>/dev/null' EXIT INT TERM
                  wait "$CH_PID"
                fi
              '')
            ] ++ config.microbe.extraRunnerBins;
          };
      };
    };
}
