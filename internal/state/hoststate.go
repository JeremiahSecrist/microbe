package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// HostStatePath is where the daemon persists host-wide (not per-stack) state:
// currently just the ULA prefix every stack's addresses are drawn from. Lives
// under /var/lib, not /run, so it survives reboots; owned by the same
// root:microbe trust boundary as the provisiond socket.
const HostStatePath = "/var/lib/microbe/host-state.json"

// HostState is durable state shared by every stack on this host.
type HostState struct {
	// ULAPrefix is the host's RFC 4193 locally-assigned /64
	// (e.g. "fd7a:3c9e:1122::/64"), generated once and never changed:
	// every stack's per-service addresses (internal/lockfile) are drawn
	// from it.
	ULAPrefix string `json:"ulaPrefix,omitempty"`
}

// LoadHostState reads the host state from path. A missing file yields an
// empty HostState, never an error.
func LoadHostState(path string) (*HostState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &HostState{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s HostState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the host state atomically (temp file + rename) as indented
// JSON, creating the parent directory if needed.
func (s *HostState) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".host-state-*.json.tmp")
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
