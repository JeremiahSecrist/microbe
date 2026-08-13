{ config, lib, pkgs, modulesPath, demoPrebuild, demoRunners, ... }:
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
    description = "Set up microbe demo directory";
    wantedBy = [ "multi-user.target" ];
    after = [ "systemd-tmpfiles-setup.service" ];
    path = [ pkgs.coreutils ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      chown -R admin:users /home/admin/microbe-demo
      chmod -R u+w /home/admin/microbe-demo

      # pgdata is excluded from the nix store copy (gitignored); create it
      # fresh so postgres can initialise it on first start.
      mkdir -p /home/admin/microbe-demo/pgdata
      chown admin:users /home/admin/microbe-demo/pgdata
      chmod 700 /home/admin/microbe-demo/pgdata
    '';
  };

  # Pre-populate the microbe runner symlinks so `microbe up` finds all three
  # services already built in the nix store and skips compilation entirely.
  # The store paths are the same derivations baked into isoImage.storeContents
  # above, so they are always present in /nix/store on the live system.
  # microbe's BuildRunner checks for an existing nix-store symlink at the
  # outLink path and returns it directly without invoking `nix build`.
  systemd.services.microbe-demo-runners = {
    description = "Pre-populate microbe runner symlinks for demo";
    wantedBy = [ "multi-user.target" ];
    after = [ "systemd-tmpfiles-setup.service" ];
    path = [ pkgs.coreutils ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      mkdir -p /var/lib/microbe/test-net/runners
      ln -sfn ${demoRunners.db}   /var/lib/microbe/test-net/runners/db
      ln -sfn ${demoRunners.web}  /var/lib/microbe/test-net/runners/web
      ln -sfn ${demoRunners.jump} /var/lib/microbe/test-net/runners/jump
    '';
  };

  microbe.demoDesktop = {
    enable = true;
    user = "admin";
    terminalCwd = "/home/admin/microbe-demo";
  };
}
