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
	for _, name := range []string{"renderer.nix", "guest-base.nix", "virtiofsd-run.nix"} {
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
		"flake.nixosModules.renderer",
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

// TestVirtiofsdRunNixHasNoRootRequirement is the regression gate for the
// live blocker this module exists to fix: microvm.nix's own generated
// virtiofsd-run hardcodes `user = "root"` in its supervisord config, which
// makes supervisord refuse to start when launched unprivileged (exactly
// how microbe launches every process). This override must never
// reintroduce that.
func TestVirtiofsdRunNixHasNoRootRequirement(t *testing.T) {
	content, err := FixedModule("virtiofsd-run.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`user = "root"`, `user="root"`} {
		if strings.Contains(content, forbidden) {
			t.Errorf("virtiofsd-run.nix contains %q; supervisord will refuse to start unprivileged", forbidden)
		}
	}
}

func TestVirtiofsdRunNixHasRequiredMarkers(t *testing.T) {
	content, err := FixedModule("virtiofsd-run.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"flake.nixosModules.virtiofsd-run",
		"microvm.binScripts.virtiofsd-run",
		"lib.mkForce",
		"config.microvm.shares",
		"--socket-path=",
		"--shared-dir=",
		"supervisord",
	} {
		if !strings.Contains(content, marker) {
			t.Errorf("virtiofsd-run.nix missing marker %q", marker)
		}
	}
}

func TestGuestBaseNixHasRequiredMarkers(t *testing.T) {
	content, err := FixedModule("guest-base.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"flake.nixosModules.guest-base",
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
