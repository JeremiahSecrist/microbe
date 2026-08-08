package flakegen

import (
	"fmt"
	"path/filepath"

	"microbe/internal/fsutil"
)

// WriteStack renders flake.nix, generated.json, the fixed modules, and one
// per-service module directly into the project directory dir (the same
// directory that holds the user's microbe.nix), so the real Nix behind a
// stack is visible and git-trackable rather than hidden in a build-only
// side directory. Each file is only written if its content actually
// changed, so unrelated files keep a stable mtime/hash across runs and
// nix's flake eval cache can hit on them.
func WriteStack(dir string, st *Stack) error {
	generated, err := st.RenderGenerated()
	if err != nil {
		return fmt.Errorf("write stack: %w", err)
	}
	files := map[string]string{
		"flake.nix":      st.RenderFlake(),
		"generated.json": generated,
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
		if _, err := fsutil.WriteFileIfChanged(p, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write stack: %w", err)
		}
	}
	return nil
}
