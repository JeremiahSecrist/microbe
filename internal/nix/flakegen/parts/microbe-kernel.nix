# microbe-kernel.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.microbe-kernel (Dendritic pattern),
# imported by both NixOS and finix ServiceParts (see flake.go).
#
# Sets boot.kernelPackages to a linux_6_12 LTS kernel trimmed for
# cloud-hypervisor microVM guests with lib.mkDefault so a service's own
# `config` block can override per-service if needed.
#
# Built with linuxManualConfig from the hand-edited microbe-kernel-6.12.config
# that lives alongside this file. linuxManualConfig bypasses nixpkgs'
# common-config.nix entirely, so we own the full kernel config without fighting
# module-system priority conflicts on hundreds of common-config sub-options.
{
  flake.nixosModules.microbe-kernel = { lib, pkgs, ... }:
    let
      microbeKernel = pkgs.linuxPackagesFor (pkgs.linuxManualConfig {
        inherit (pkgs.linuxKernel.kernels.linux_6_12) src modDirVersion version;
        configfile = ./microbe-kernel-6.12.config;
      });
    in
    {
      boot.kernelPackages = lib.mkDefault microbeKernel;
    };
}
