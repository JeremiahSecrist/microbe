package flakegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedStackEvaluates is the M2 exit-criteria check: the rendered
// stack must evaluate against real microvm.nix so that every service has a
// declaredRunner derivation. It only evaluates (cheap); it does not realize
// the closure. Skipped when nix is unavailable.
func TestGeneratedStackEvaluates(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	userPath := filepath.Join(dir, "src.nix")
	user, err := os.ReadFile("testdata/microbe.nix")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, user, 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustStack(t, fixtureConfig())
	if err := WriteStack(dir, st, userPath); err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{"db", "jump", "web"} {
		target := ".#nixosConfigurations." + svc + ".config.microvm.declaredRunner"
		cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
		cmd.Dir = dir
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("service %s did not evaluate: %v\n%s", svc, err, stderr.String())
		}
		if !strings.Contains(string(out), "microvm") {
			t.Errorf("service %s: declaredRunner = %s, want a microvm store path", svc, out)
		}
	}
}
