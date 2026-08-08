// Package state persists the CLI lifecycle state in .microbe/state.json (§9.4).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// NetworkState records one provisioned network's subnet and its IP allocations.
type NetworkState struct {
	CIDR      string            `json:"cidr"`
	Allocated map[string]string `json:"allocated"`
}

// ServiceState records what a started service looks like on this host.
type ServiceState struct {
	IP      map[string]string `json:"ip"`
	CID     int               `json:"cid"`
	MACs    map[string]string `json:"macs"`
	Volumes []string          `json:"volumes"`
	Status  string            `json:"status"`
	PID     int               `json:"pid"`
	Runner  string            `json:"runner"`

	// VirtiofsdPID is the companion virtiofsd process's pid, or 0 if the
	// service has no virtiofs shares (see internal/runtime.StartVirtiofsd).
	VirtiofsdPID int `json:"virtiofsdPid,omitempty"`
}

// PortState records the DNAT rule for one published host port.
type PortState struct {
	Service string `json:"svc"`
	Guest   int    `json:"guest"`
}

// Store is the on-disk CLI state (spec §9.4).
type Store struct {
	Stack    string                  `json:"stack"`
	Networks map[string]NetworkState `json:"networks"`
	Services map[string]ServiceState `json:"services"`
	Ports    map[string]PortState    `json:"ports"`
}

// Load reads the store from path. A missing file yields an empty store, never
// an error.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the store atomically (temp file + rename) as indented JSON,
// creating the parent directory if needed.
func (s *Store) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.json.tmp")
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
