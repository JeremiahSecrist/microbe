{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
in
{
  flake.nixosConfigurations.web = inputs.nixpkgs.lib.nixosSystem {
    system = "x86_64-linux";
    modules = [
      inputs.microvm.nixosModules.microvm
      config.flake.nixosModules.renderer
      config.flake.nixosModules.guest-base
      config.flake.nixosModules.virtiofsd-run
      { microCompose.serviceName = "web"; }
      (compose.services.web.config or ({ ... }: { }))
    ];
  };
}
