package flakegen

import (
	"fmt"
	"path/filepath"

	"microbe/internal/fsutil"
)

// WriteStack renders flake.nix, generated.json, the fixed parts, and one
// per-service part directly into the project directory dir (the same
// directory that holds the user's microbe.nix), so the real Nix behind a
// stack is visible and git-trackable rather than hidden in a build-only
// side directory. Each file is only written if its content actually
// changed, so unrelated files keep a stable mtime/hash across runs and
// nix's flake eval cache can hit on them. Follows the Dendritic pattern:
// flake.nix (flake-parts + import-tree) auto-imports everything under
// parts/, each file self-registering rather than being wired here.
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
		files["parts/"+name] = content
	}
	agentFiles, err := AgentSource()
	if err != nil {
		return fmt.Errorf("write stack: %w", err)
	}
	for name, content := range agentFiles {
		files["agent/"+name] = content
	}
	for _, name := range st.Names() {
		files["parts/"+name+".nix"] = ServicePart(name)
	}
	for path, content := range files {
		p := filepath.Join(dir, path)
		if _, err := fsutil.WriteFileIfChanged(p, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write stack: %w", err)
		}
	}
	return nil
}
