package state

import (
	"path/filepath"
	"testing"
)

func TestHostStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-state.json")

	s := &HostState{ULAPrefix: "fd7a:3c9e:1122::/64"}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := LoadHostState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ULAPrefix != s.ULAPrefix {
		t.Errorf("ULAPrefix = %q, want %q", got.ULAPrefix, s.ULAPrefix)
	}
}

func TestLoadHostStateMissingFileYieldsEmpty(t *testing.T) {
	got, err := LoadHostState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got.ULAPrefix != "" {
		t.Errorf("ULAPrefix = %q, want empty for missing file", got.ULAPrefix)
	}
}
