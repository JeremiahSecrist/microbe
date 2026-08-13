package nix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildRunnerUsesGivenTarget proves BuildRunner builds whatever nix
// attrpath it's given instead of a hardcoded nixosConfigurations path --
// finix services need .#finixConfigurations.<svc>.config.microbe.qemuRunner
// (see flakegen/stack.go's buildTarget), not
// .#nixosConfigurations.<svc>.config.microvm.declaredRunner. Faking `nix`
// on PATH to record its argv avoids a real build.
//
// Also verifies that ".#attr" is rewritten to "path:<dir>#attr" so nix reads
// freshly written parts files from the filesystem rather than via git+file://,
// which only sees tracked files (see installable() in nix.go).
func TestBuildRunnerUsesGivenTarget(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	fakeNix := filepath.Join(dir, "nix")
	script := "#!/bin/sh\necho \"$@\" > " + argvFile + "\necho /fake/out/path\n"
	if err := os.WriteFile(fakeNix, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	target := ".#finixConfigurations.a.config.microbe.qemuRunner"
	wantArg := "path:" + dir + "#finixConfigurations.a.config.microbe.qemuRunner"
	if _, err := BuildRunner(dir, target, filepath.Join(dir, "out"), nil); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Fields(string(got))
	found := false
	for _, a := range argv {
		if a == wantArg {
			found = true
		}
	}
	if !found {
		t.Errorf("nix invoked with argv %v, want arg %q", argv, wantArg)
	}
}
