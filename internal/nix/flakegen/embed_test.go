package flakegen

import (
	"strings"
	"testing"
)

func TestFixedModulesEmbedded(t *testing.T) {
	mods, err := FixedModules()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"renderer.nix", "guest-base.nix"} {
		content, ok := mods[name]
		if !ok {
			t.Errorf("missing fixed module %q", name)
			continue
		}
		if content == "" {
			t.Errorf("fixed module %q is empty", name)
		}
	}
}

func TestRendererNixHasRequiredMarkers(t *testing.T) {
	content, err := FixedModule("renderer.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"microCompose.serviceName",
		"builtins.fromJSON (builtins.readFile ../generated.json)",
		"import ../microbe.nix",
		"vsock.cid = gen.cid",
		"systemd.network.networks = gen.networkd",
		"environment.etc.hosts = lib.mkForce",
		"gen.volumes",
		"imageType = \"raw\"",
		"autoCreate = false",
	} {
		if !strings.Contains(content, marker) {
			t.Errorf("renderer.nix missing marker %q", marker)
		}
	}
}

func TestGuestBaseNixHasRequiredMarkers(t *testing.T) {
	content, err := FixedModule("guest-base.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"services.openssh",
		"microvm.optimize.enable",
		"builtins.fromJSON (builtins.readFile ../generated.json)",
		"users.users.root.openssh.authorizedKeys.keys",
	} {
		if !strings.Contains(content, marker) {
			t.Errorf("guest-base.nix missing marker %q", marker)
		}
	}
}
