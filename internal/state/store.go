// Package state persists the CLI lifecycle state in .microbe/state.json (§9.4).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// ServiceState records what a started service looks like on this host.
type ServiceState struct {
	// ID is a stable identity assigned the first time this service name is
	// started, and carried forward on every later rebuild of the store
	// (including across Stale transitions) so a VM can be tracked by
	// identity even as its PID changes on restart.
	ID string `json:"id,omitempty"`

	// Addr is this service's one IPv6 address (see internal/lockfile),
	// shared across every network below.
	Addr string `json:"addr"`
	// Networks is the set of network names this service is attached to --
	// needed to reconstruct its bridges/taps (see hostnet.BridgeName /
	// flakegen.TapID) without a config, since Addr no longer varies per
	// network the way it did under the old per-network IPv4 model.
	Networks []string          `json:"networks,omitempty"`
	CID      int               `json:"cid"`
	MACs     map[string]string `json:"macs"`
	Volumes  []string          `json:"volumes"`
	Status   string            `json:"status"`
	PID      int               `json:"pid"`
	Runner   string            `json:"runner"`

	// VirtiofsdPID is the companion virtiofsd process's pid, or 0 if the
	// service has no virtiofs shares (see internal/runtime.StartVirtiofsd).
	VirtiofsdPID int `json:"virtiofsdPid,omitempty"`

	// Stale is true when this entry was carried forward from a prior state
	// because the service is no longer named in the current config. down
	// still tears it down by PID; up never resurrects it.
	Stale bool `json:"stale,omitempty"`
}

// PortState records the DNAT rule for one published host port.
type PortState struct {
	Service  string `json:"svc"`
	Guest    int    `json:"guest"`
	ProxyPID int    `json:"proxyPid,omitempty"`
}

// Store is the on-disk CLI state (spec §9.4).
type Store struct {
	Stack string `json:"stack"`

	// Networks is every network name this stack has provisioned, so device
	// names (hostnet.BridgeName) can be reconstructed without a config
	// (e.g. host-wide purge of a stack microbe isn't currently pointed at).
	Networks []string                `json:"networks,omitempty"`
	Services map[string]ServiceState `json:"services"`
	Ports    map[string]PortState    `json:"ports"`

	// Provisioned records the host interface devices (bridges and taps)
	// microbe has created for this stack, so `down` can sweep any that the
	// current config no longer names (spec §8.6 orphan sweep). Exact names
	// only: hashed br-*/mvc-* names can't be attributed by prefix alone.
	Provisioned []string `json:"provisioned,omitempty"`
}

// Load reads the store from path. A missing file yields an empty store, never
// an error. If the file was written by a pre-IPv6-migration binary (v0
// format: networks as an object map, services with per-network "ip" fields
// instead of a single "addr"), it is transparently migrated to the current
// schema in memory; the on-disk file is not rewritten.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	if isV0Format(data) {
		return migrateV0(data)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// isV0Format reports whether data was written by the pre-IPv6-migration
// binary. The distinguishing marker is the "networks" field being a JSON
// object ({"frontend":{...}}) instead of a JSON array (["frontend"]).
func isV0Format(data []byte) bool {
	var probe struct {
		Networks json.RawMessage `json:"networks"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || len(probe.Networks) == 0 {
		return false
	}
	return probe.Networks[0] == '{'
}

// migrateV0 parses the pre-IPv6-migration state format and returns an
// equivalent Store in the current schema:
//
//   - networks: object map → sorted []string of keys
//   - services[x].ip: per-network map → Networks []string (keys only); Addr
//     is left empty because IPv4 stacks have no single IPv6 address.
func migrateV0(data []byte) (*Store, error) {
	var raw struct {
		Stack    string                     `json:"stack"`
		Networks map[string]json.RawMessage `json:"networks"`
		Services map[string]struct {
			IP           map[string]string `json:"ip"`
			CID          int               `json:"cid"`
			MACs         map[string]string `json:"macs"`
			Volumes      []string          `json:"volumes"`
			Status       string            `json:"status"`
			PID          int               `json:"pid"`
			Runner       string            `json:"runner"`
			VirtiofsdPID int               `json:"virtiofsdPid,omitempty"`
			Stale        bool              `json:"stale,omitempty"`
		} `json:"services"`
		Ports       map[string]PortState `json:"ports"`
		Provisioned []string             `json:"provisioned,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	store := &Store{
		Stack:       raw.Stack,
		Ports:       raw.Ports,
		Provisioned: raw.Provisioned,
		Services:    make(map[string]ServiceState, len(raw.Services)),
	}

	for name := range raw.Networks {
		store.Networks = append(store.Networks, name)
	}
	sort.Strings(store.Networks)

	for name, svc := range raw.Services {
		networks := make([]string, 0, len(svc.IP))
		for netName := range svc.IP {
			networks = append(networks, netName)
		}
		sort.Strings(networks)

		store.Services[name] = ServiceState{
			Addr:         "",
			Networks:     networks,
			CID:          svc.CID,
			MACs:         svc.MACs,
			Volumes:      svc.Volumes,
			Status:       svc.Status,
			PID:          svc.PID,
			Runner:       svc.Runner,
			VirtiofsdPID: svc.VirtiofsdPID,
			Stale:        svc.Stale,
		}
	}

	return store, nil
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
