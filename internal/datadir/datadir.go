// Package datadir locates the daemon-owned runtime directory for a stack:
// ssh keys, disk volumes, logs, VM run sockets and state.json. Mirrors
// /var/lib/docker — root:microbe owned (see modules/host.nix), setgid so
// group members can create their stack's subdir without daemon involvement.
package datadir

import "path/filepath"

// Root is the daemon-owned base directory provisioned by the microbe NixOS
// module (systemd.tmpfiles.rules in modules/host.nix). A var, not a const,
// so tests can point it at a throwaway directory instead of the real
// system path.
var Root = "/var/lib/microbe"

// Dir returns the runtime data directory for the stack named name.
func Dir(name string) string {
	return filepath.Join(Root, name)
}
