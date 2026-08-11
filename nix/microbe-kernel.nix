# Returns a linux_6_12 LTS linuxPackages set trimmed for cloud-hypervisor
# microVM guests, built with linuxManualConfig from a hand-edited .config.
#
# linuxManualConfig is used instead of linux.override { structuredExtraConfig }
# because structuredExtraConfig merges with nixpkgs' common-config.nix, which
# sets hundreds of options at normal module-system priority. Overriding those
# requires lib.mkForce on every conflicting sub-option, and disabling a parent
# driver (e.g. CFG80211) makes its common-config sub-options Kconfig-invisible,
# triggering hard "option set but not in .config" build errors. linuxManualConfig
# bypasses common-config.nix entirely: we own the full .config, run olddefconfig
# once to resolve dependencies, and build from the result.
#
# The base config is nixpkgs' linux_6_12 config (generated from its
# structuredExtraConfig + common-config.nix), edited to:
#
#   - Disable XEN (KVM hypervisor, Xen stubs are dead weight)
#   - Disable CFG80211 / MAC80211 (entire 802.11 WiFi stack — no RF hardware)
#   - Disable BT (Bluetooth — no RF hardware)
#   - Disable physical Ethernet NICs (only virtio_net is wired)
#   - Disable GPU drivers: DRM_I915, DRM_AMDGPU, DRM_NOUVEAU, DRM_VMWGFX
#   - Disable InfiniBand, Firewire, PCMCIA
#   - Disable physical sensor drivers (HWMON parent stays; individual chips gone)
#   - Disable unused filesystems: XFS, JFS, HFS, 9P, AFS, OVERLAY
#   - Promote virtio drivers from =m to =y (built-in before init, no modprobe)
#
# Pinned to linux_6_12 LTS so NixOS and finix guests build the same kernel
# regardless of which nixpkgs revision the project uses.
{ pkgs, lib }:
pkgs.linuxPackagesFor (pkgs.linuxManualConfig {
  inherit (pkgs.linuxKernel.kernels.linux_6_12) src modDirVersion version;
  configfile = ./microbe-kernel-6.12.config;
})
