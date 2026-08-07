package nix

import (
	"os"
	"path/filepath"
	"testing"
)

// stageSource must copy exactly the flake source files and skip runtime
// artifacts. This is what lets BuildRunner build from a temp dir outside the
// git work tree, where git-based flake resolution would otherwise drop the
// gitignored .microbe/ directory.
func TestStageSourceCopiesOnlySource(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"flake.nix":      "{ inputs = {}; }",
		"microbe.nix":    "{ name = \"x\"; }",
		"generated.nix":  "{ services = {}; }",
		"modules/db.nix": "{ microCompose.serviceName = \"db\"; }",
		"state.json":     "{\"stack\":\"x\"}",
	}
	for path, content := range files {
		p := filepath.Join(src, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "volumes", "stack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "volumes", "stack", "big.qcow2"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := stageSource(src, dst); err != nil {
		t.Fatal(err)
	}

	for path := range files {
		if path == "state.json" {
			continue
		}
		want := files[path]
		got, err := os.ReadFile(filepath.Join(dst, path))
		if err != nil {
			t.Errorf("staged %s: %v", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("staged %s = %q, want %q", path, got, want)
		}
	}
	for _, absent := range []string{"state.json", "volumes/stack/big.qcow2"} {
		if _, err := os.Stat(filepath.Join(dst, absent)); err == nil {
			t.Errorf("stage must not contain %s", absent)
		}
	}
}
