// Package sshkey manages the ed25519 keypair microbe uses to reach guests
// (exec/shell), generating it once per stack and reusing it thereafter.
package sshkey

import (
	"os"
	"path/filepath"
	"strings"

	"microbe/internal/cmdrun"
)

// keyName is the private key filename within dir; the public key is
// keyName + ".pub", matching ssh-keygen's own convention.
const keyName = "id_ed25519"

// EnsureKeypair returns the path to the private key in dir and the public
// key's authorized_keys line (trimmed), generating a fresh ed25519 keypair
// via run if one does not already exist.
func EnsureKeypair(run cmdrun.Runner, dir string) (privPath, pubKey string, err error) {
	privPath = filepath.Join(dir, keyName)
	pubPath := privPath + ".pub"

	if _, statErr := os.Stat(privPath); statErr != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", err
		}
		if err := run("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "microbe", "-f", privPath); err != nil {
			return "", "", err
		}
	}

	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		return "", "", err
	}
	return privPath, strings.TrimSpace(string(pubBytes)), nil
}
