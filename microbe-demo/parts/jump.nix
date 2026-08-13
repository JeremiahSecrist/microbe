{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
  pkgs = inputs.nixpkgs.legacyPackages.x86_64-linux;
in
{
  flake.nixosConfigurations.jump = inputs.nixpkgs.lib.nixosSystem {
    system = "x86_64-linux";
    modules = [
      { nixpkgs.pkgs = pkgs; }
      inputs.microvm.nixosModules.microvm
      config.flake.nixosModules.renderer
      config.flake.nixosModules.guest-base
      config.flake.nixosModules.virtiofsd-run
      config.flake.nixosModules.agent
      config.flake.nixosModules.microbe-kernel
      { microCompose.serviceName = "jump"; }
      (compose.services.jump.config or ({ ... }: { }))
    ];
  };
}
