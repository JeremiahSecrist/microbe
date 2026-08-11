# microbe-kernel.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.microbe-kernel (Dendritic pattern),
# imported by both NixOS and finix ServiceParts (see flake.go).
#
# Sets boot.kernelPackages to a kernel trimmed for cloud-hypervisor microVM
# guests with lib.mkDefault so a service's own `config` block can override
# per-service if needed.
#
# Must be self-contained: embedded parts are shipped in the CLI binary and
# written verbatim into generated projects' parts/ dirs — they cannot import
# from the microbe repo's nix/ directory. The structuredExtraConfig here
# mirrors nix/microbe-kernel.nix exactly.
#
# Four categories of changes:
#   1. Verified-safe filesystem/device trims (ported from finix-base.nix's
#      boot.kernelPatches, now shared across both guest OS types)
#   2. Xen stubs removed (all `option yes` in nixpkgs common-config.nix)
#   3. virtio drivers promoted =m → =y (faster boot, no initramfs modprobe)
#   4. Physical hardware drivers disabled (no physical hardware in a microVM;
#      parent subsystems like SCSI_LOWLEVEL forced by common-config, but
#      individual drivers are defconfig-origin and overrideable)
{
  flake.nixosModules.microbe-kernel = { lib, pkgs, ... }:
    let
      microbeKernel = pkgs.linuxPackagesFor (pkgs.linuxKernel.kernels.linux_6_12.override {
        structuredExtraConfig = with lib.kernel; {

          # ── Category 1: verified-safe filesystem / device trims ──────────
          XFS_FS = no;
          JFS_FS = no;
          HFS_FS = no;
          HFSPLUS_FS = no;
          "9P_FS" = no;
          AFS_FS = no;
          OVERLAY_FS = no;
          KEYBOARD_ATKBD = no;
          BLK_DEV_DM = no;
          BLK_DEV_LOOP = no;

          # ── Category 2: Xen ────────────────────────────────────────────
          # lib.mkForce needed: pkgs.linux.override goes through the
          # kernel's own module system where `option yes` from common-config
          # conflicts with a plain `no` at the same priority. Sub-options
          # (XEN_BALLOON, HVC_XEN_FRONTEND, etc.) must NOT be set here —
          # they become Kconfig-invisible when XEN=n, causing a hard
          # "unused option" build error if explicitly specified.
          XEN = lib.mkForce no;

          # ── Category 3: virtio built-in (promote defconfig =m → =y) ─────
          VIRTIO_PCI = yes;
          VIRTIO_NET = yes;
          VIRTIO_CONSOLE = yes;
          FUSE_FS = yes;
          VIRTIO_FS = yes;
          VSOCKETS = yes;
          VIRTIO_VSOCKETS = yes;
          VIRTIO_BLK = yes;
          KVM_GUEST = yes;

          # ── Category 4: physical hardware drivers ────────────────────────
          # Physical NICs — only virtio_net is wired
          E1000 = no;
          E1000E = no;
          IGB = no;
          IGBVF = no;
          IXGBE = no;
          IXGBEVF = no;
          R8169 = no;
          TULIP = no;
          PCNET32 = no;
          BNX2X = no;
          SFC = no;
          SKY2 = no;
          ATL1 = no;
          ATL2 = no;

          # Physical CPU/board sensors (HWMON parent forced, drivers from defconfig)
          SENSORS_CORETEMP = no;
          SENSORS_K10TEMP = no;
          SENSORS_W83627HF = no;
          SENSORS_IT87 = no;
        };
      });
    in
    {
      boot.kernelPackages = lib.mkDefault microbeKernel;
    };
}
