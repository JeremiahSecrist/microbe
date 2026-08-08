package flakegen

import "strings"

// RenderFlake emits the generated stack flake (spec §9.3): one nixosSystem
// per service, all importing the renderer, guest base, the user config, and
// generated.json.
func (st *Stack) RenderFlake() string {
	var b strings.Builder
	b.WriteString(`{
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
      nixosConfigurations = builtins.mapAttrs (name: _: mkSvc name) `)
	b.WriteString(servicesAttrset(st))
	b.WriteString(";\n    };\n}\n")
	return b.String()
}

func servicesAttrset(st *Stack) string {
	names := sortedServiceNames(st)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + " = null"
	}
	return "{ " + strings.Join(parts, "; ") + "; }"
}

// ServiceModule emits the per-service module that tells the renderer which
// slice of generated.json and microbe.nix this configuration is.
func ServiceModule(name string) string {
	return "{ ... }:\n{\n  microCompose.serviceName = " + nixQuote(name) + ";\n}\n"
}

// nixQuote renders s as a double-quoted Nix string literal. name is
// regex-validated (see config.Validate) to [a-z][a-z0-9_-]*, so this only
// needs to escape the two characters that could ever appear literally.
func nixQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
