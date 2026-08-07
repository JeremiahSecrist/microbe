package config

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func Eval(path string) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	expr := fmt.Sprintf(projectionTemplate, nixString(abs))
	cmd := exec.Command("nix", "eval", "--impure", "--json", "--expr", expr)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("config: nix eval failed: %v\n%s", err, stderr.String())
	}
	return out, nil
}

const projectionTemplate = `let
  lib = (import (builtins.getFlake "nixpkgs").outPath {}).lib;
  f = import %s;
  compose = if builtins.isFunction f then f { inherit lib; } else f;
  proj = s: (builtins.removeAttrs s [ "config" ]) //
    (if builtins.hasAttr "config" s then { configPresent = true; } else {});
in {
  schemaVersion = 1;
  name = compose.name or "";
  networks = compose.networks or {};
  services = builtins.mapAttrs (_: proj) (compose.services or {});
}`

func nixString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
