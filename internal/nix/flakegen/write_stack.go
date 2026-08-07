package flakegen

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteStack renders the full .microbe/ tree for a stack: flake.nix,
// microbe.nix (the user's compose file, verbatim), generated.nix, the fixed
// modules, and one per-service module. Idempotent.
func WriteStack(dir string, st *Stack, userConfigPath string) error {
	generated, err := st.RenderGenerated()
	if err != nil {
		return fmt.Errorf("write stack: %w", err)
	}
	files := map[string]string{
		"flake.nix":     st.RenderFlake(),
		"generated.nix": generated,
	}
	mods, err := FixedModules()
	if err != nil {
		return fmt.Errorf("write stack: %w", err)
	}
	for name, content := range mods {
		files["modules/"+name] = content
	}
	for _, name := range st.Names() {
		files["modules/"+name+".nix"] = ServiceModule(name)
	}
	for path, content := range files {
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write stack: %w", err)
		}
	}
	user, err := os.ReadFile(userConfigPath)
	if err != nil {
		return fmt.Errorf("write stack: read %s: %w", userConfigPath, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), user, 0o644); err != nil {
		return fmt.Errorf("write stack: %w", err)
	}
	return nil
}
