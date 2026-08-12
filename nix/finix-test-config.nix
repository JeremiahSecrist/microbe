# Standalone finix test VM config for nix run .#microvm-finix.
#
# Sets config.microbe.finix.* with hardcoded values so finix-base.nix can be
# imported without a compose file or generated.json. Also overrides
# microbe.qemuRunner to start virtiofsd inline and handle snapshot/restore --
# production stacks use `microbe up` for virtiofsd lifecycle management and
# will get snapshot/restore from finix-base.nix's runner directly, but nix run
# needs everything in one self-contained script.
#
# Uses a stable run dir ($HOME/.cache/microbe/finix-test-run) rather than
# mktemp so that virtiofsd socket absolute paths are identical across
# invocations: cloud-hypervisor embeds socket paths in the snapshot, so the
# restore path must find virtiofsd at the same absolute path.
{ config, lib, pkgs, ... }:
let
  svcName = "finix-test";
  cid = 4;    # cid=3 is the NixOS test VM; cid=4 is distinct

  # Snapshot key: 32-char nix-store hash of the guest system derivation.
  # Rotating (config change) triggers a fresh boot + new snapshot automatically.
  snapKey = builtins.substring 11 32 config.system.topLevel;

  # Nix-store paths baked at build time (safe to escapeShellArg).
  chBin        = "${pkgs.cloud-hypervisor}/bin/cloud-hypervisor";
  chRemote     = "${pkgs.cloud-hypervisor}/bin/ch-remote";
  socat        = "${pkgs.socat}/bin/socat";
  virtiofsdBin = lib.getExe pkgs.virtiofsd;
  vmlinux      = "${config.boot.kernelPackages.kernel.dev}/vmlinux";
  initrd       = "${config.boot.initrd.package}/initrd";
  topLevel     = config.system.topLevel;
  cmdline      = "console=ttyS0 quiet loglevel=3 reboot=t panic=-1 "
    + "8250.nr_uarts=1 cryptomgr.notests=1 no_timer_check mitigations=off "
    + "random.trust_cpu=on "
    + "init=${topLevel}/init "
    + toString config.boot.kernelParams;
in
{
  users.users.root.password = "";

  users.users.admin = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    password = "";
  };

  # No physical keyboard in a microVM.
  hardware.console.enable = false;

  programs.sudo.enable = true;
  environment.etc."sudoers".text = lib.mkAfter ''
    Defaults:%wheel !authenticate
  '';

  microbe.finix = {
    inherit svcName cid;
    vcpu       = 1;
    mem        = 512;
    userShares = [ ];
  };

  # microbe-agent: guest-side backend for `microbe exec`/`microbe shell`.
  # Built from microbe-demo/agent (the written-out copy with a real go.mod)
  # rather than via finix-agent.nix's ../agent relative path which doesn't
  # resolve when this file is imported from the repo root's flake.nix.
  # The agent listens on vsock port 6969; the vsock device is wired by
  # finix-base.nix's --vsock cid=...,socket=notify.vsock argument.
  finit.services.microbe-agent =
    let
      agentPkg = pkgs.buildGoModule {
        pname = "microbe-agent";
        version = "1";
        src = ../microbe-demo/agent;
        vendorHash = null;
        env.CGO_ENABLED = 0;
      };
    in {
      command = "${agentPkg}/bin/microbe-agent";
      runlevels = "1234";
      respawn = true;
      restart_sec = 1;
    };

  # Dummy user service at the default runlevel [234] -- starts AFTER the
  # snapshot point (runlevel 1) so it never pollutes the snapshot state.
  finit.services.test-user-svc = {
    command = pkgs.writeShellScript "test-user-svc" ''
      echo "test-user-svc started at $(date)" >> /tmp/test-user-svc.log
      exec sleep infinity
    '';
    respawn = true;
  };

  # Override the runner produced by finix-base.nix (mkForce beats normal priority).
  microbe.qemuRunner = lib.mkForce (pkgs.writeShellScriptBin "microvm-run" ''
    # Runtime paths -- derived from $HOME so they survive across shell invocations
    # with the same absolute values, which is the key requirement for snapshot/restore.
    RUN_DIR="$HOME/.cache/microbe/finix-test-run"
    SNAP_DIR="$HOME/.cache/microbe/finix-test-snapshots/${snapKey}"
    mkdir -p "$RUN_DIR"

    VIRTIOFS_SOCK="$RUN_DIR/${svcName}-virtiofs-nix-store.sock"
    VSOCK_PATH="$RUN_DIR/notify.vsock"
    API_SOCK="$RUN_DIR/${svcName}.sock"

    # Probe microbe-agent on hybrid-vsock port 6969.  Succeeds once finit has
    # fully booted and the agent is accepting connections.
    probe_agent() {
      printf 'CONNECT 6969\n' \
        | ${lib.escapeShellArg socat} -t1 UNIX-CONNECT:"$VSOCK_PATH",connect-timeout=1 - \
            2>/dev/null \
        | grep -q "^OK"
    }

    # Remove stale sockets and virtiofsd pid lock from a prior run.
    rm -f "$VIRTIOFS_SOCK" "$VIRTIOFS_SOCK.pid" "$API_SOCK" "$API_SOCK.lock" "$VSOCK_PATH"

    ${lib.escapeShellArg virtiofsdBin} \
      --socket-path "$VIRTIOFS_SOCK" \
      --shared-dir /nix/store \
      --cache always &
    VIRTIOFSD_PID=$!

    while [ ! -S "$VIRTIOFS_SOCK" ]; do sleep 0.05; done

    if [ -d "$SNAP_DIR" ]; then
      # ── RESTORE PATH (subsequent runs) ──────────────────────────────────────
      # virtiofsd is running at the same socket path embedded in the snapshot;
      # cloud-hypervisor reconnects automatically and resumes in <100 ms.
      # --serial/--console/--kernel must NOT be passed with --restore: CH hangs
      # if --kernel is combined with --restore (v53 behavior), and --serial/
      # --console alone require --kernel. The VM runs headlessly; access via
      # `microbe exec` (vsock) instead of the serial console.
      cleanup() { kill "$VIRTIOFSD_PID" 2>/dev/null; }
      trap cleanup EXIT INT TERM
      exec ${lib.escapeShellArg chBin} \
        --api-socket "$API_SOCK" \
        --restore "source_url=file://$SNAP_DIR,resume=true" \
        "$@"
    else
      # ── FIRST-BOOT PATH ─────────────────────────────────────────────────────
      # Boot normally, wait for finit + agent, snapshot, resume.
      ${lib.escapeShellArg chBin} \
        --cpus boot=1 \
        --kernel ${lib.escapeShellArg vmlinux} \
        --initramfs ${lib.escapeShellArg initrd} \
        --cmdline ${lib.escapeShellArg cmdline} \
        --seccomp true \
        --watchdog \
        --memory size=512M,shared=on \
        --console null \
        --serial tty \
        --vsock "cid=${toString cid},socket=$VSOCK_PATH" \
        --api-socket "$API_SOCK" \
        --fs "tag=nix-store,socket=$VIRTIOFS_SOCK" \
        "$@" &
      CH_PID=$!
      cleanup() {
        kill "$VIRTIOFSD_PID" 2>/dev/null
        kill "$CH_PID" 2>/dev/null
        wait "$CH_PID" 2>/dev/null
        rm -rf "$SNAP_DIR.tmp"
      }
      trap cleanup EXIT INT TERM

      # Wait for cloud-hypervisor API socket to appear.
      while [ ! -S "$API_SOCK" ]; do sleep 0.05; done

      # Wait for microbe-agent on vsock 6969.  Agent runs at finit runlevel 1,
      # before user services at runlevel 2 -- snapshot here captures a clean
      # pre-user-service state so every restore starts user services fresh.
      until probe_agent; do sleep 0.1; done

      # Pause → snapshot → resume.  .tmp dir avoids leaving a partial snapshot
      # that a subsequent run would try (and fail) to restore from.
      mkdir -p "$SNAP_DIR.tmp"
      ${lib.escapeShellArg chRemote} --api-socket "$API_SOCK" pause
      ${lib.escapeShellArg chRemote} --api-socket "$API_SOCK" \
        snapshot "file://$SNAP_DIR.tmp"
      mv "$SNAP_DIR.tmp" "$SNAP_DIR"
      # Prune snapshots from old system configs.
      find "$HOME/.cache/microbe/finix-test-snapshots" -maxdepth 1 -mindepth 1 -type d \
        ! -name '${snapKey}' -exec rm -rf {} + 2>/dev/null || true
      ${lib.escapeShellArg chRemote} --api-socket "$API_SOCK" resume

      # Snapshot done; keep kill-on-exit but drop the rm-on-exit part.
      trap 'kill "$VIRTIOFSD_PID" 2>/dev/null; kill "$CH_PID" 2>/dev/null; wait "$CH_PID" 2>/dev/null' EXIT INT TERM
      wait "$CH_PID"
    fi
  '');
}
