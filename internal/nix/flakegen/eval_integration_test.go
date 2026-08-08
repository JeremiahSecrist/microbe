package flakegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withVolumeImages populates the generated image path for the db-data disk
// declared in testdata/microbe.nix, matching what up.go would compute from
// the CLI's data dir.
func withVolumeImages(dataDir string, st *Stack) {
	db := st.Services["db"]
	db.VolumeImages = map[string]string{
		"db-data": filepath.Join(dataDir, "volumes", "db-data.qcow2"),
	}
	st.Services["db"] = db
}

// TestGeneratedStackEvaluates is the M2 exit-criteria check: the rendered
// stack must evaluate against real microvm.nix so that every service has a
// declaredRunner derivation. It only evaluates (cheap); it does not realize
// the closure. Skipped when nix is unavailable.
func TestGeneratedStackEvaluates(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	user, err := os.ReadFile("testdata/microbe.nix")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), user, 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustStack(t, fixtureConfig())
	withVolumeImages(dir, st)
	if err := WriteStack(dir, st); err != nil {
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

// TestDefaultHypervisorIsCloudHypervisor pins the renderer's default when a
// compose service omits `hypervisor`: it must fall back to cloud-hypervisor
// (matching config.DefaultHypervisor), not qemu.
func TestDefaultHypervisorIsCloudHypervisor(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	user, err := os.ReadFile("testdata/microbe.nix")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), user, 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustStack(t, fixtureConfig())
	withVolumeImages(dir, st)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := ".#nixosConfigurations.db.config.microvm.hypervisor"
	cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("eval %s: %v\n%s", target, err, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != `"cloud-hypervisor"` {
		t.Errorf("default hypervisor = %s, want \"cloud-hypervisor\"", got)
	}
}
