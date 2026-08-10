package nix

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// BuildRunner realizes target (a nix attrpath, e.g.
// flakegen.Stack.Services[svc].BuildTarget — nixos and finix guests build
// different attrpaths, see stack.go's buildTarget) by running `nix build`
// directly against dir, symlinking the result to outLink. Returns the store
// path of the built runner.
//
// dir is the real project directory holding flake.nix/microbe.nix/parts
// (see internal/nix/flakegen.WriteStack) — no staging copy is needed since
// those files are visible, real project files rather than a build-only
// side directory.
func BuildRunner(dir, target, outLink string) (string, error) {
	absOut, err := filepath.Abs(outLink)
	if err != nil {
		return "", fmt.Errorf("nix: resolve out-link %s: %w", outLink, err)
	}
	cmd := exec.Command("nix", "build", "--no-write-lock-file", "--print-out-paths", "--out-link", absOut, target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix build %s failed: %w\n%s", target, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}
