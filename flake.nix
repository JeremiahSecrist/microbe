{
  description = "Minimal NixOS ISO with SSH root (password) login and passwordless wheel admin";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    microvm = {
      url = "github:microvm-nix/microvm.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, microvm }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      # The microbe CLI, built from this repo. Dependencies are vendored in
      # ./vendor (vendorHash = null).
      microbePkg = pkgs.buildGoModule {
        pname = "microbe";
        version = "0.1.0";
        src = ./.;
        vendorHash = null;
      };

      # The ISO image is built from this configuration. It is the intended
      # microbe host, so it imports the host module and installs the CLI.
      iso = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [
          ./iso-config.nix
          ./modules/host.nix
          {
            virtualisation.microbe.enable = true;
            virtualisation.microbe.package = microbePkg;
          }
        ];
      };

      # The MicroVM is built from the same shared SSH/admin config
      # (./base-config.nix) so both targets expose identical logins.
      microvmCfg = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [
          microvm.nixosModules.microvm
          ./microvm-config.nix
        ];
      };
    in
    {
      packages.${system} = {
        microbe = microbePkg;
        iso = iso.config.system.build.isoImage;
        microvm = microvmCfg.config.microvm.declaredRunner;
        default = iso.config.system.build.isoImage;
      };

      # NixOS module configuring a host to run microbe VMs.
      nixosModules.host = import ./modules/host.nix;

      apps.${system} = {
        microvm = {
          type = "app";
          program = "${microvmCfg.config.microvm.declaredRunner}/bin/microvm-run";
        };
      };

      checks.${system} = {
        # Full verification: configures, builds and boots a system graph then
        # packages it into a bootable ISO.
        iso = iso.config.system.build.isoImage;
        # Faster sanity check: verifies the whole NixOS system graph builds
        # without producing the ISO filesystem image.
        toplevel = iso.config.system.build.toplevel;
        # Verifies the MicroVM runner graph builds.
        microvm = microvmCfg.config.microvm.declaredRunner;
      };

      nixosConfigurations = {
        iso = iso;
        microvm = microvmCfg;
      };
    };
}
