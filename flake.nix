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

      # The ISO image is built from this configuration.
      iso = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [ ./iso-config.nix ];
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
        iso = iso.config.system.build.isoImage;
        microvm = microvmCfg.config.microvm.declaredRunner;
        default = iso.config.system.build.isoImage;
      };

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