package flakegen

import (
	"embed"
	"fmt"
)

//go:embed parts/*
var fixedModules embed.FS

// FixedModules returns the fixed Nix modules shipped with the CLI, keyed by
// filename (e.g. "renderer.nix"). These are written verbatim into the
// project's parts/ dir by the build command.
func FixedModules() (map[string]string, error) {
	entries, err := fixedModules.ReadDir("parts")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := fixedModules.ReadFile("parts/" + e.Name())
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

// FixedModule returns a single fixed module by filename.
func FixedModule(name string) (string, error) {
	b, err := fixedModules.ReadFile("parts/" + name)
	if err != nil {
		return "", fmt.Errorf("flakegen: %w", err)
	}
	return string(b), nil
}
