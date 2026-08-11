{
  description = "Minimal NixOS ISO with SSH root (password) login and passwordless wheel admin";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    microvm = {
      url = "github:microvm-nix/microvm.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    finix = {
      url = "github:finix-community/finix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, microvm, finix }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      microbeKernelPackages = import ./nix/microbe-kernel.nix {
        inherit pkgs;
        lib = nixpkgs.lib;
      };

      # The microbe CLI, built from this repo. Dependencies are fetched from
      # the go module proxy into the nix store at build time (see vendorHash),
      # not committed under ./vendor. microbe-demo/ is excluded: it's a
      # sample compose project living inside this repo for dogfooding, and
      # `microbe up`/`build` there writes real files into it (flake.nix,
      # generated.json, agent/{go.mod,main.go} -- see internal/nix/flakegen's
      # WriteStack). Once agent/go.mod exists on disk it's a second Go
      # module nested in the source tree, which buildGoModule's subpackage
      # discovery doesn't skip the way plain `go build ./...` does, so
      # without this filter the CLI's own build breaks the moment the demo
      # gets built once.
      microbePkg = pkgs.buildGoModule {
        pname = "microbe";
        version = "0.1.0";
        src = pkgs.lib.cleanSourceWith {
          src = ./.;
          filter = path: _type: !pkgs.lib.hasPrefix (toString ./microbe-demo) path;
        };
        vendorHash = "sha256-ip4BmPWbK58kyKxIBejB3kRMjRvntXVWxHz4+SejoHg=";
        # go test needs network (vsock/pasta) for some tests; sandbox has no
        # network access, so tests hang until sandbox setup times out. Skip.
        doCheck = false;
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
          { boot.kernelPackages = microbeKernelPackages; }
        ];
      };

      # Standalone finix test VM -- no compose/generated files needed.
      # finix-base.nix reads config.microbe.finix.* which finix-test-config.nix
      # sets directly with hardcoded values. finix-compose.nix is intentionally
      # omitted (that module reads the compose file; it's only imported in
      # generated service stacks via flake.go's ServicePart).
      finixTestCfg = finix.lib.finixSystem {
        lib = nixpkgs.lib;
        specialArgs.pkgs = pkgs;
        modules = [
          finix.nixosModules.getty
          { boot.kernelPackages = microbeKernelPackages; }
          (import ./internal/nix/flakegen/parts/finix-base.nix).flake.nixosModules.finix-base
          (import ./internal/nix/flakegen/parts/finix-virtiofsd-run.nix).flake.nixosModules.finix-virtiofsd-run
          ./nix/finix-test-config.nix
        ];
      };
    in
    {
      packages.${system} = {
        microbe = microbePkg;
        iso = iso.config.system.build.isoImage;
        microvm = microvmCfg.config.microvm.declaredRunner;
        microvm-kernel = microbeKernelPackages.kernel;
        microvm-finix = finixTestCfg.config.microbe.qemuRunner;
        default = iso.config.system.build.isoImage;
      };

      # NixOS module configuring a host to run microbe VMs.
      nixosModules.host = import ./modules/host.nix;

      apps.${system} = {
        microvm-nixos = {
          type = "app";
          program = "${microvmCfg.config.microvm.declaredRunner}/bin/microvm-run";
        };
        microvm-finix = {
          type = "app";
          program = "${finixTestCfg.config.microbe.qemuRunner}/bin/microvm-run";
        };
        # Keep the old name working.
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
        microvm-finix = finixTestCfg.config.microbe.qemuRunner;
      };

      nixosConfigurations = {
        iso = iso;
        microvm = microvmCfg;
      };
    };
}
