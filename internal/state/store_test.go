package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := &Store{
		Stack: "test-net",
		Networks: map[string]NetworkState{
			"backend": {
				CIDR:      "192.168.51.0/24",
				Allocated: map[string]string{"db": "192.168.51.2", "web": "192.168.51.3"},
			},
		},
		Services: map[string]ServiceState{
			"db": {
				IP:      map[string]string{"backend": "192.168.51.2"},
				CID:     3,
				MACs:    map[string]string{"backend": "02:00:00:00:00:01"},
				Volumes: []string{"db-data"},
				Status:  "running",
				PID:     4242,
				Runner:  ".microbe/runners/db",
			},
		},
		Ports: map[string]PortState{
			"5432": {Service: "db", Guest: 5432},
		},
	}

	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, s)
	}
}

func TestLoadMissingReturnsEmptyStore(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if got.Stack != "" || got.Networks != nil || got.Services != nil || got.Ports != nil {
		t.Errorf("Load(missing) = %+v, want empty store", got)
	}
}

func TestLoadCorruptReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load(corrupt): want error, got nil")
	}
}

func TestSaveIsAtomicAndMkdirsParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "state.json")

	s := &Store{Stack: "x"}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "{\n") {
		t.Errorf("state.json not indented: %q", string(data))
	}

	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("leftover temp files: %v", matches)
	}
}
