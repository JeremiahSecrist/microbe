{ config, lib, pkgs, modulesPath, ... }:

{
  imports = [
    # Shared SSH/admin configuration, identical to the live ISO target.
    ./base-config.nix
  ];

  networking.hostName = "microvm";

  # NixOS's default availableKernelModules includes physical-hardware drivers
  # (atkbd, ahci, etc.) that don't exist in the trimmed microVM kernel.
  # Restrict to what's actually present and relevant.
  boot.initrd.availableKernelModules = lib.mkForce [ "erofs" ];


  microvm = {
    hypervisor = "cloud-hypervisor";
    vcpu = 2;
    mem = 1024;
    vsock.cid = 3;
  };

  networking.useDHCP = false;
}
