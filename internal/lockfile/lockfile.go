// Package lockfile persists a stack's permanent per-service IPv6 addresses
// in a committed, non-gitignored file (microbe.lock.json, next to
// microbe.yml). Unlike internal/state.Store (runtime-only, gitignored,
// rebuilt freely), a Lock is the source of truth for "has this service
// already been assigned an address": once written, an entry is never
// regenerated, so addresses stay stable across machines and are meant to be
// checked into version control like a Cargo.lock/package-lock.json.
package lockfile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Lock is the on-disk address assignment record for one stack.
type Lock struct {
	// Prefix is the host's persisted ULA /64 (see internal/state host
	// prefix, added in a later stage) that every address below was drawn
	// from.
	Prefix string `json:"prefix,omitempty"`

	// Services maps service name to its permanently assigned IPv6 address
	// (bare address, no prefix length suffix).
	Services map[string]string `json:"services,omitempty"`
}

// Load reads the lock from path. A missing file yields an empty Lock, never
// an error, so a stack's first `up` can populate it from scratch.
func Load(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Lock{Services: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	if l.Services == nil {
		l.Services = map[string]string{}
	}
	return &l, nil
}

// Save writes the lock atomically (temp file + rename) as indented JSON,
// creating the parent directory if needed.
func (l *Lock) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lock-*.json.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
