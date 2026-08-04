{
  description = "Minimal NixOS ISO with SSH root (password) login and passwordless wheel admin";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";

      # The ISO image is built from this configuration.
      iso = nixpkgs.lib.nixosSystem {
        inherit system;
        # specialArgs = { inherit nixpkgs; };
        modules = [ ./iso-config.nix ];
      };
    in
    {
      packages.${system} = {
        iso = iso.config.system.build.isoImage;
        default = iso.config.system.build.isoImage;
      };

      checks.${system} = {
        # Full verification: configures, builds and boots a system graph then
        # packages it into a bootable ISO.
        iso = iso.config.system.build.isoImage;
        # Faster sanity check: verifies the whole NixOS system graph builds
        # without producing the ISO filesystem image.
        toplevel = iso.config.system.build.toplevel;
      };

      nixosConfigurations.iso = iso;
    };
}
