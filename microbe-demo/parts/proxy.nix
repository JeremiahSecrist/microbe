{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
  pkgs = inputs.nixpkgs.legacyPackages.x86_64-linux;
in
{
  flake.finixConfigurations.proxy = inputs.finix.lib.finixSystem {
    lib = inputs.nixpkgs.lib;
    specialArgs = { inherit pkgs; };
    modules = [
      inputs.finix.nixosModules.getty
      config.flake.nixosModules.finix-compose
      config.flake.nixosModules.finix-base
      config.flake.nixosModules.finix-agent
      config.flake.nixosModules.finix-network
      config.flake.nixosModules.finix-virtiofsd-run
      config.flake.nixosModules.microbe-kernel
      { microCompose.serviceName = "proxy"; }
      (compose.services.proxy.config or ({ ... }: { }))
    ];
  };
}
