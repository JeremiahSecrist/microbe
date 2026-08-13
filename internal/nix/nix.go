package nix

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
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
// statusFn, if non-nil, is called with the human-readable name of each
// derivation as nix begins building it (e.g. "linux-6.12.100"). It is called
// from the same goroutine as the caller, so it must not block indefinitely.
//
// dir is the real project directory holding flake.nix/microbe.nix/parts
// (see internal/nix/flakegen.WriteStack) — no staging copy is needed since
// those files are visible, real project files rather than a build-only
// side directory.
func BuildRunner(dir, target, outLink string, statusFn func(string)) (string, error) {
	absOut, err := filepath.Abs(outLink)
	if err != nil {
		return "", fmt.Errorf("nix: resolve out-link %s: %w", outLink, err)
	}
	// If a pre-populated symlink to a nix store path already exists at the
	// outLink location (e.g., placed by the demo ISO activation service),
	// return it directly without invoking `nix build`. This lets the demo ISO
	// skip all compilation: the runners are already in the store.
	if dest, err := os.Readlink(absOut); err == nil && strings.HasPrefix(dest, "/nix/store/") {
		return dest, nil
	}
	cmd := exec.Command("nix", "build", "--no-write-lock-file", "--print-out-paths", "--out-link", absOut, target)
	cmd.Dir = dir

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("nix: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("nix build %s: %w", target, err)
	}

	var stderr strings.Builder
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		line := scanner.Text()
		stderr.WriteString(line + "\n")
		if statusFn != nil {
			if pkg := parseNixBuildLine(line); pkg != "" {
				statusFn(pkg)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("nix build %s failed: %w\n%s", target, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// parseNixBuildLine extracts a human-readable package name from a nix build
// stderr line of the form: building '/nix/store/<hash>-<name>.drv'...
// Returns "" if the line is not a build-start line.
func parseNixBuildLine(line string) string {
	const prefix = "building '"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	path := strings.TrimPrefix(line, prefix)
	if i := strings.IndexByte(path, '\''); i >= 0 {
		path = path[:i]
	}
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, ".drv")
	// Strip the 32-char nix hash prefix + dash: "k3xc0npl6v2bihrrfhq8dwa89565zsah-linux-..."
	if i := strings.IndexByte(name, '-'); i >= 0 {
		name = name[i+1:]
	}
	return name
}
