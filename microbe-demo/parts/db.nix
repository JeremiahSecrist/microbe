{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
in
{
  flake.nixosConfigurations.db = inputs.nixpkgs.lib.nixosSystem {
    system = "x86_64-linux";
    modules = [
      inputs.microvm.nixosModules.microvm
      config.flake.nixosModules.renderer
      config.flake.nixosModules.guest-base
      { microCompose.serviceName = "db"; }
      (compose.services.db.config or ({ ... }: { }))
    ];
  };
}
