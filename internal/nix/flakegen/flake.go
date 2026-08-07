package flakegen

import "strings"

// RenderFlake emits the generated stack flake (spec §9.3): one nixosSystem
// per service, all importing the renderer, guest base, the user config, and
// generated.nix.
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
      nixosConfigurations = builtins.mapAttrs (_: mkSvc) `)
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
// slice of generated.nix and microbe.nix this configuration is.
func ServiceModule(name string) string {
	return "{ ... }:\n{\n  microCompose.serviceName = " + nixQuote(name) + ";\n}\n"
}
