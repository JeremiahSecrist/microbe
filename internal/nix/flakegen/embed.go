package flakegen

import (
	"embed"
	"fmt"
)

//go:embed parts/*
var fixedModules embed.FS

// agentSource is the guest agent's dependency-free Go source (see
// agent/main.go's doc comment), written verbatim into each project's
// agent/ dir by WriteStack so parts/agent.nix can build it locally with
// pkgs.buildGoModule and no module fetching. It has no go.mod of its own
// in this tree -- go:embed refuses to embed a file that belongs to a
// different module, so agentGoMod is synthesized as a plain Go string
// instead (see AgentSource) and only ever exists as a real file in a
// generated project's agent/ dir, never here.
//
//go:embed agent/main.go
var agentSource embed.FS

// agentGoMod is the guest agent's synthesized go.mod: no requires, since
// it's built dependency-free (see agent/main.go's doc comment).
const agentGoMod = "module microbe-agent\n\ngo 1.22\n"

// AgentSource returns the guest agent's Go source files, keyed by filename
// relative to agent/ (e.g. "go.mod", "main.go").
func AgentSource() (map[string]string, error) {
	main, err := agentSource.ReadFile("agent/main.go")
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"go.mod":  agentGoMod,
		"main.go": string(main),
	}, nil
}

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
