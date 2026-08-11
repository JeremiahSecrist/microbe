# Standalone finix test VM config for nix run .#microvm-finix.
#
# Sets config.microbe.finix.* with hardcoded values so finix-base.nix can be
# imported without a compose file or generated.json. Also overrides
# microbe.qemuRunner to start virtiofsd inline before exec'ing cloud-hypervisor
# -- production stacks use `microbe up` for virtiofsd lifecycle management, but
# nix run needs everything in one script.
{ config, lib, pkgs, ... }:
let
  svcName = "finix-test";
  cid = 4;    # cid=3 is the NixOS test VM; cid=4 is distinct
  virtiofsSocket = tag: "${svcName}-virtiofs-${tag}.sock";
  vsockPath = "notify.vsock";

  argv = [
    "${pkgs.cloud-hypervisor}/bin/cloud-hypervisor"
    "--cpus" "boot=1"
    "--kernel" "${config.boot.kernelPackages.kernel.dev}/vmlinux"
    "--initramfs" "${config.boot.initrd.package}/initrd"
    "--cmdline" ("earlyprintk=ttyS0 console=ttyS0 reboot=t panic=-1 "
      + "init=${config.system.topLevel}/init "
      + toString config.boot.kernelParams)
    "--seccomp" "true"
    "--watchdog"
    "--memory" "size=512M,shared=on"
    "--console" "null"
    "--serial" "tty"
    "--vsock" "cid=${toString cid},socket=${vsockPath}"
    "--api-socket" "${svcName}.sock"
    "--fs" "tag=nix-store,socket=${virtiofsSocket "nix-store"}"
  ];
in
{
  users.users.root.password = "";

  users.users.admin = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    password = "";
  };

  environment.systemPackages = [ pkgs.sudo ];
  environment.etc."sudoers".text = "%wheel ALL=(ALL:ALL) NOPASSWD: ALL\n";

  microbe.finix = {
    inherit svcName cid;
    vcpu       = 1;
    mem        = 512;
    userShares = [ ];
  };

  # Override the runner produced by finix-base.nix. mkForce wins over
  # finix-base.nix's plain assignment (which has normal module priority).
  # Starts virtiofsd in the background, waits for its socket, then execs
  # cloud-hypervisor -- mirrors what `microbe up` does via StartVirtiofsd +
  # StartService, but collapsed into one script for nix run compatibility.
  microbe.qemuRunner = lib.mkForce (pkgs.writeShellScriptBin "microvm-run" ''
    RUNDIR=$(mktemp -d)
    cd "$RUNDIR"
    cleanup() { kill "$VIRTIOFSD_PID" 2>/dev/null; rm -rf "$RUNDIR"; }
    trap cleanup EXIT INT TERM

    ${lib.getExe pkgs.virtiofsd} \
      --socket-path ${virtiofsSocket "nix-store"} \
      --shared-dir /nix/store \
      --cache auto &
    VIRTIOFSD_PID=$!

    while [ ! -S ${virtiofsSocket "nix-store"} ]; do sleep 0.05; done

    exec ${lib.concatMapStringsSep " " lib.escapeShellArg argv} "$@"
  '');
}
