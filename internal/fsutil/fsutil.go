// Package fsutil provides small filesystem helpers shared across microbe.
package fsutil

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileIfChanged writes content to path only if it differs from the
// file's current contents (or the file doesn't exist yet), leaving mtime
// untouched otherwise. This lets downstream content-hash caches — notably
// nix's flake eval cache — hit on files that didn't actually change between
// runs, instead of treating every render as a fresh input.
func WriteFileIfChanged(path string, content []byte, perm os.FileMode) (changed bool, err error) {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("fsutil: read %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("fsutil: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return false, fmt.Errorf("fsutil: write %s: %w", path, err)
	}
	return true, nil
}
