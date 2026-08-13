{ config, lib, pkgs, modulesPath, demoPrebuild, ... }:
let
  # Prebuild the microbe guest kernel so microbe up finds it already in the
  # store and skips compilation. Both this flake and the microbe-demo flake
  # pin the same nixpkgs rev, so the derivation is identical.
  microbeKernel = (pkgs.linuxPackagesFor (pkgs.linuxManualConfig {
    inherit (pkgs.linuxKernel.kernels.linux_6_12) src modDirVersion version;
    configfile = ./microbe-demo/parts/microbe-kernel-6.12.config;
  })).kernel;
in

{
  imports = [
    # Makes `system.build.isoImage` available and produces a bootable,
    # writable (tmpfs root + overlay) live ISO. Unlike the full installer CD
    # modules it does not add a default `nixos` user or alter passwords.
    (modulesPath + "/installer/cd-dvd/iso-image.nix")
    # Shared SSH/admin configuration, identical to the plain ISO/MicroVM
    # targets.
    ./base-config.nix
    ./modules/desktop.nix
  ];

  isoImage.volumeID = "MICROBE-DEMO";
  isoImage.makeEfiBootable = true;
  isoImage.makeUsbBootable = true;
  isoImage.storeContents = [ microbeKernel ] ++ demoPrebuild;

  # --- Networking -----------------------------------------------------------
  networking.networkmanager.enable = true;

  # --- Demo content -----------------------------------------------------------
  # Copy microbe-demo/ into the admin home and make it fully writable.
  # tmpfiles `C` copies from the Nix store but inherits read-only permissions
  # (444/555); microbe up needs to write pgdata/ and generated files, so we
  # fix ownership and permissions in a oneshot service that runs right after.
  systemd.tmpfiles.rules = [
    "C /home/admin/microbe-demo - admin users - ${./microbe-demo}"
  ];

  systemd.services.microbe-demo-writable = {
    description = "Make microbe demo directory writable";
    wantedBy = [ "multi-user.target" ];
    after = [ "systemd-tmpfiles-setup.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = [
        "${pkgs.coreutils}/bin/chown -R admin:users /home/admin/microbe-demo"
        "${pkgs.coreutils}/bin/chmod -R u+w /home/admin/microbe-demo"
      ];
    };
  };

  microbe.demoDesktop = {
    enable = true;
    user = "admin";
    terminalCwd = "/home/admin/microbe-demo";
  };
}
