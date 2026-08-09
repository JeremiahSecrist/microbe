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
	got := ServicePart("db")
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
		t.Errorf("ServicePart(\"db\") =\n%s\nwant:\n%s", got, want)
	}
}
