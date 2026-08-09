package flakegen

import (
	"os"
	"testing"
)

func TestRenderFlakeMatchesGolden(t *testing.T) {
	st := mustStack(t, fixtureConfig())
	got := st.RenderFlake()
	want, err := os.ReadFile("testdata/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("RenderFlake():\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestServicePart(t *testing.T) {
	got := ServicePart("db", "nixos")
	want := `{ inputs, config, ... }:
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
      config.flake.nixosModules.virtiofsd-run
      config.flake.nixosModules.agent
      { microCompose.serviceName = "db"; }
      (compose.services.db.config or ({ ... }: { }))
    ];
  };
}
`
	if got != want {
		t.Errorf("ServicePart(\"db\", \"nixos\") =\n%s\nwant:\n%s", got, want)
	}
}

// TestServicePartFinix proves a finix service renders a finixSystem call
// (finix.lib.finixSystem, per the real flake's signature -- no `system`
// arg, lib passed explicitly) instead of nixosSystem, registered under
// flake.finixConfigurations rather than flake.nixosConfigurations.
func TestServicePartFinix(t *testing.T) {
	got := ServicePart("db", "finix")
	want := `{ inputs, config, ... }:
let
  compose = import ../microbe.nix;
in
{
  flake.finixConfigurations.db = inputs.finix.lib.finixSystem {
    lib = inputs.nixpkgs.lib;
    specialArgs.pkgs = import inputs.nixpkgs { system = "x86_64-linux"; };
    modules = [
      (inputs.finix + "/modules/virtualisation/qemu.nix")
      config.flake.nixosModules.finix-base
      (compose.services.db.config or ({ ... }: { }))
    ];
  };
}
`
	if got != want {
		t.Errorf("ServicePart(\"db\", \"finix\") =\n%s\nwant:\n%s", got, want)
	}
}
