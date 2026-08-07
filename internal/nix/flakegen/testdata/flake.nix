{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    microvm.url = "github:microvm-nix/microvm.nix";
  };

  outputs = { nixpkgs, microvm, ... }:
    let
      system = "x86_64-linux";

      mkSvc = name:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            microvm.nixosModules.microvm
            ./modules/renderer.nix
            ./modules/guest-base.nix
            ./modules/${name}.nix
            (import ./microbe.nix)
            ./generated.nix
          ];
        };
    in
    {
      nixosConfigurations = builtins.mapAttrs (_: mkSvc) { db = null; jump = null; web = null; };
    };
}
