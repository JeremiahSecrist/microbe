package flakegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteStackWritesAllArtifacts(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "src.nix")
	if err := os.WriteFile(userPath, []byte("{ name = \"test-net\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustStack(t, fixtureConfig())

	if err := WriteStack(dir, st, userPath); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{
		"flake.nix",
		"generated.nix",
		"microbe.nix",
		"modules/renderer.nix",
		"modules/guest-base.nix",
		"modules/db.nix",
		"modules/jump.nix",
		"modules/web.nix",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing artifact %s: %v", f, err)
		}
	}

	user, err := os.ReadFile(filepath.Join(dir, "microbe.nix"))
	if err != nil {
		t.Fatal(err)
	}
	if string(user) != "{ name = \"test-net\"; }\n" {
		t.Errorf("microbe.nix = %q, want copied user file", user)
	}

	gen, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "generated.nix"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != gen {
		t.Errorf("generated.nix differs from RenderGenerated()")
	}
}

func TestWriteStackRejectsMissingUserFile(t *testing.T) {
	dir := t.TempDir()
	st := mustStack(t, fixtureConfig())
	if err := WriteStack(dir, st, filepath.Join(dir, "nope.nix")); err == nil {
		t.Error("WriteStack with missing user file: want error, got nil")
	}
}
