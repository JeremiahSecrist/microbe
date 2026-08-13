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
    finix.url = "github:finix-community/finix";
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

// ServicePart emits the per-service flake-parts module for name, branching
// on os ("nixos" or "finix"):
//
//   - "nixos": the whole nixosSystem, self-registered as
//     flake.nixosConfigurations.<name>. config.flake.nixosModules.
//     renderer/.guest-base/.virtiofsd-run/.agent resolve to the fixed
//     modules' own self-registered values (flake-parts' shared
//     module-system config, no explicit imports needed — same mechanism as
//     the Dendritic pattern's cross-part references).
//   - "finix": a finixSystem call, self-registered as
//     flake.finixConfigurations.<name>. finix.lib.finixSystem takes no
//     `system` arg and has no implicit nixpkgs of its own (the finix flake
//     input declares none) — lib and pkgs must be supplied explicitly via
//     specialArgs, unlike nixosSystem above. inputs.finix.nixosModules.getty
//     is included explicitly: unlike core services (dbus/elogind/mdevd/...),
//     getty isn't in finix's own nixosModules.default import list (see its
//     modules/default.nix), so without this a finix guest boots to runlevel
//     2 with no console-attached shell at all — not a hang, just nothing
//     left to show on the console once boot finishes (verified live: this
//     is what a genuinely-idle-with-no-getty finix guest looks like).
func ServicePart(name, os string) string {
	q := nixQuote(name)
	if os == "finix" {
		return `{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
  pkgs = inputs.nixpkgs.legacyPackages.x86_64-linux;
in
{
  flake.finixConfigurations.` + name + ` = inputs.finix.lib.finixSystem {
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
      { microCompose.serviceName = ` + q + `; }
      (compose.services.` + name + `.config or ({ ... }: { }))
    ];
  };
}
`
	}
	return `{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
  pkgs = inputs.nixpkgs.legacyPackages.x86_64-linux;
in
{
  flake.nixosConfigurations.` + name + ` = inputs.nixpkgs.lib.nixosSystem {
    system = "x86_64-linux";
    modules = [
      { nixpkgs.pkgs = pkgs; }
      inputs.microvm.nixosModules.microvm
      config.flake.nixosModules.renderer
      config.flake.nixosModules.guest-base
      config.flake.nixosModules.virtiofsd-run
      config.flake.nixosModules.agent
      config.flake.nixosModules.microbe-kernel
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
