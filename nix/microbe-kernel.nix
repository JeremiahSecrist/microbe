# Returns a linux_6_12 LTS linuxPackages set trimmed for cloud-hypervisor
# microVM guests. Pinned to 6.12 LTS so both NixOS and finix guests build
# the exact same kernel regardless of which nixpkgs revision they use.
#
# Four categories of config changes:
#
# 1. Verified-safe filesystem/device trims — root is tmpfs, store/shares
#    are virtiofs; these drivers are unreachable. Confirmed against
#    nixpkgs build and live /proc/modules on a booted finix guest.
#    Ported from finix-base.nix's boot.kernelPatches so both guest types
#    now benefit.
#
# 2. Xen disabled. cloud-hypervisor is a KVM hypervisor; Xen stubs are
#    dead weight. common-config.nix sets XEN as `option yes` — in the
#    kernel's own module system this conflicts with a plain `no` at the
#    same priority, so lib.mkForce is required. Sub-options (XEN_DOM0,
#    HVC_XEN_FRONTEND, etc.) become invisible to Kconfig when XEN=n and
#    must NOT be listed here — nixpkgs' kernel builder treats any option
#    that was set but doesn't appear in the final .config as a hard error.
#
# 3. virtio drivers promoted =m → =y. x86_64 defconfig builds virtio as
#    modules; built-in means they're active before init — no initramfs
#    modprobe delay. Safe because structuredExtraConfig overrides defconfig
#    values and none of these appear in common-config.nix at normal priority.
#
# 4. Physical hardware drivers disabled. A microVM has no physical NICs,
#    SCSI HBAs, or sensor chips. Individual drivers come from defconfig
#    (not common-config.nix) and override cleanly. Parent subsystems
#    (SCSI_LOWLEVEL, ATA_BMDMA, HWMON) are forced by common-config.nix
#    and cannot be removed here without conflicts.
#
# ignoreConfigErrors is deliberately not set — the kernel builder catches
# bad options at build time, before any boot attempt.
{ pkgs, lib }:
pkgs.linuxPackagesFor (pkgs.linuxKernel.kernels.linux_6_12.override {
  structuredExtraConfig = with lib.kernel; {

    # ── Category 1: verified-safe filesystem / device trims ──────────────
    # Root is tmpfs, store and shares are virtiofs — none of the below are
    # reachable. Safe set confirmed against nixpkgs build (see finix-base.nix
    # comment for everything that was tried and failed).
    XFS_FS = no;
    JFS_FS = no;
    HFS_FS = no;
    HFSPLUS_FS = no;
    "9P_FS" = no;
    AFS_FS = no;
    OVERLAY_FS = no;
    KEYBOARD_ATKBD = no; # PS/2 keyboard controller, no physical hardware
    BLK_DEV_DM = no;     # device mapper / LVM, never used in this guest
    BLK_DEV_LOOP = no;   # loop devices, never created

    # ── Category 2: Xen ──────────────────────────────────────────────────
    XEN = lib.mkForce no; # see file-level comment for why mkForce is needed

    # ── Category 3: virtio built-in (promote defconfig =m → =y) ─────────
    VIRTIO_PCI = yes;
    VIRTIO_NET = yes;
    VIRTIO_CONSOLE = yes;
    FUSE_FS = yes;       # virtiofs depends on FUSE
    VIRTIO_FS = yes;     # virtiofs transport
    VSOCKETS = yes;      # AF_VSOCK socket family
    VIRTIO_VSOCKETS = yes; # vsock over virtio (guest-side driver)
    VIRTIO_BLK = yes;    # block device (disk volume support)
    KVM_GUEST = yes;     # PV clock, steal time, PV spinlocks

    # ── Category 4: physical hardware drivers ────────────────────────────
    # No physical hardware exists in a cloud-hypervisor microVM. These are
    # all defconfig-origin options not set in common-config.nix, so they
    # override cleanly. Parent subsystems (SCSI_LOWLEVEL, ATA_BMDMA,
    # HWMON) are forced by common-config.nix and cannot be removed here;
    # see the plan for the linuxManualConfig escape hatch if needed.

    # Physical NICs — only virtio_net is wired
    E1000 = no;      # Intel PRO/1000 PCI
    E1000E = no;     # Intel PRO/1000 PCIe
    IGB = no;        # Intel Gigabit
    IGBVF = no;      # Intel Gigabit VF (SR-IOV, host-side concept)
    IXGBE = no;      # Intel 10GbE
    IXGBEVF = no;    # Intel 10GbE VF
    R8169 = no;      # Realtek 8169/8168/8101
    TULIP = no;      # DEC/Intel Tulip
    PCNET32 = no;    # AMD PCnet32
    BNX2X = no;      # Broadcom NetXtreme II
    SFC = no;        # Solarflare SFC9000
    SKY2 = no;       # Marvell Yukon 2
    ATL1 = no;       # Atheros L1
    ATL2 = no;       # Atheros L2

    # Physical CPU/board sensors — HWMON parent forced, individual drivers
    # are defconfig-origin
    SENSORS_CORETEMP = no; # Intel Core temperature
    SENSORS_K10TEMP = no;  # AMD K10/Zen temperature
    SENSORS_W83627HF = no; # Winbond hardware monitor
    SENSORS_IT87 = no;     # ITE sensor chip
  };
})
