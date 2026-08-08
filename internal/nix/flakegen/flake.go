package flakegen

import "strings"

// RenderFlake emits the generated stack flake (spec §9.3) in the Dendritic
// pattern: flake-parts + import-tree auto-import every file under ./parts,
// each of which self-registers into flake.nixosModules.* or
// flake.nixosConfigurations.* (see ServicePart, and the fixed
// renderer.nix/guest-base.nix modules) rather than being wired here by an
// explicit imports list. Static — doesn't depend on st, since no service is
// enumerated at this level anymore.
func (st *Stack) RenderFlake() string {
	return `{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    microvm.url = "github:microvm-nix/microvm.nix";
    flake-parts.url = "github:hercules-ci/flake-parts";
    import-tree.url = "github:vic/import-tree";
  };

  outputs = inputs@{ flake-parts, import-tree, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" ];
      imports = (import-tree ./parts).imports;
    };
}
`
}

// ServicePart emits the per-service flake-parts module: the whole
// nixosSystem for name, self-registered as flake.nixosConfigurations.<name>
// rather than built by a shared factory function. config.flake.nixosModules.
// renderer/.guest-base resolve to the fixed modules' own self-registered
// values (flake-parts' shared module-system config, no explicit imports
// needed — same mechanism as the Dendritic pattern's cross-part references).
func ServicePart(name string) string {
	q := nixQuote(name)
	return `{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
in
{
  flake.nixosConfigurations.` + name + ` = inputs.nixpkgs.lib.nixosSystem {
    system = "x86_64-linux";
    modules = [
      inputs.microvm.nixosModules.microvm
      config.flake.nixosModules.renderer
      config.flake.nixosModules.guest-base
      { microCompose.serviceName = ` + q + `; }
      (compose.services.` + name + `.config or ({ ... }: { }))
    ];
  };
}
`
}

// nixQuote renders s as a double-quoted Nix string literal. name is
// regex-validated (see config.Validate) to [a-z][a-z0-9_-]*, so this only
// needs to escape the two characters that could ever appear literally.
func nixQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
