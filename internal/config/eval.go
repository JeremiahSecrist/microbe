package config

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Eval runs the Nix module at path through the Compose projection and
// returns its result as JSON, ready for Parse. path must end in ".nix"
// (see Load, which dispatches here for Nix sources and reads JSON files
// directly otherwise).
func Eval(path string) ([]byte, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	expr := fmt.Sprintf(projectionTemplate, CurrentSchemaVersion, nixString(absPath))
	cmd := exec.Command("nix", "eval", "--impure", "--json", "--expr", expr)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	jsonOut, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("config: nix eval failed: %w\n%s", err, stderr.String())
	}
	return jsonOut, nil
}

// projectionTemplate is a Nix expression that imports the module at %[2]s,
// evaluates it as a Compose stack, and reshapes the result into the JSON
// document Parse expects: services carry a configPresent flag instead of
// their raw "config" attribute, and schemaVersion is stamped as %[1]d.
const projectionTemplate = `let
  lib = (import (builtins.getFlake "nixpkgs").outPath {}).lib;
  f = import %[2]s;
  compose = if builtins.isFunction f then f { inherit lib; } else f;
  proj = s: (builtins.removeAttrs s [ "config" ]) //
    (if builtins.hasAttr "config" s then { configPresent = true; } else {});
in {
  schemaVersion = %[1]d;
  name = compose.name or "";
  networks = compose.networks or {};
  services = builtins.mapAttrs (_: proj) (compose.services or {});
}`

// nixString renders raw as a double-quoted Nix string literal, escaping
// backslashes and quotes.
func nixString(raw string) string {
	escaper := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + escaper.Replace(raw) + `"`
}
