package nix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BuildRunner realizes the declaredRunner derivation for a service inside the
// generated flake dir, symlinking the result to outLink. Returns the store
// path of the built runner.
//
// The flake is built from a staged copy outside the git work tree: a path
// input inside a git repo is resolved via git, and gitignored directories
// (like .microbe/) are excluded from that tree, which would make the flake
// appear to have no flake.nix.
func BuildRunner(dir, svc, outLink string) (string, error) {
	target := ".#nixosConfigurations." + svc + ".config.microvm.declaredRunner"

	stage, err := os.MkdirTemp("", "microbe-build-")
	if err != nil {
		return "", fmt.Errorf("nix: create build stage dir: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := stageSource(dir, stage); err != nil {
		return "", err
	}

	absOut, err := filepath.Abs(outLink)
	if err != nil {
		return "", fmt.Errorf("nix: resolve out-link %s: %w", outLink, err)
	}
	cmd := exec.Command("nix", "build", "--no-write-lock-file", "--print-out-paths", "--out-link", absOut, target)
	cmd.Dir = stage
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix build %s failed: %w\n%s", svc, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

// stageSource copies exactly the flake source files (flake.nix, microbe.nix,
// generated.nix, modules/) from src into dst, skipping runtime artifacts such
// as volumes/, runners/, logs/, runs/ and state.json.
func stageSource(src, dst string) error {
	for _, rel := range []string{"flake.nix", "microbe.nix", "generated.nix"} {
		if err := copyFile(filepath.Join(src, rel), filepath.Join(dst, rel)); err != nil {
			return err
		}
	}
	modsSrc := filepath.Join(src, "modules")
	entries, err := os.ReadDir(modsSrc)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("nix: read %s: %w", modsSrc, err)
	}
	modsDst := filepath.Join(dst, "modules")
	if err := os.MkdirAll(modsDst, 0o755); err != nil {
		return fmt.Errorf("nix: create %s: %w", modsDst, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".nix") {
			continue
		}
		if err := copyFile(filepath.Join(modsSrc, e.Name()), filepath.Join(modsDst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("nix: read %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("nix: create %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("nix: write %s: %w", dst, err)
	}
	return nil
}
