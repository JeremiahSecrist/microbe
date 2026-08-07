package nix

import (
	"fmt"
	"os/exec"
	"strings"
)

// BuildRunner realizes the declaredRunner derivation for a service inside the
// generated flake dir, symlinking the result to outLink. Returns the store
// path of the built runner.
func BuildRunner(dir, svc, outLink string) (string, error) {
	target := ".#nixosConfigurations." + svc + ".config.microvm.declaredRunner"
	cmd := exec.Command("nix", "build", "--no-write-lock-file", "--out-link", outLink, target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix build %s failed: %v\n%s", svc, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}
