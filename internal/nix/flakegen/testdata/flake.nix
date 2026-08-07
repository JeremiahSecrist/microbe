{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    microvm.url = "github:microvm-nix/microvm.nix";
  };

  outputs = { nixpkgs, microvm, ... }:
    let
      system = "x86_64-linux";
      compose = import ./microbe.nix;

      mkSvc = name:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            microvm.nixosModules.microvm
            ./modules/renderer.nix
            ./modules/guest-base.nix
            ./modules/${name}.nix
            (compose.services.${name}.config or ({ ... }: { }))
          ];
        };
    in
    {
      nixosConfigurations = builtins.mapAttrs (name: _: mkSvc name) { db = null; jump = null; web = null; };
    };
}
