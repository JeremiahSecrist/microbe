package cmd

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"microbe/internal/state"
)

func writeStateForPs(t *testing.T, base string, services map[string]state.ServiceState) {
	t.Helper()
	store := &state.Store{
		Stack:    "test-net",
		Networks: map[string]state.NetworkState{},
		Services: services,
		Ports:    map[string]state.PortState{},
	}
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}
}

func loadPsState(t *testing.T, base string) *state.Store {
	t.Helper()
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPsReconcilesRunningVM(t *testing.T) {
	base := t.TempDir()
	writeStateForPs(t, base, map[string]state.ServiceState{
		"db": {Status: serviceStatusHealthy, PID: 1234},
	})

	origVMState := vmState
	vmState = func(string) (string, error) { return "Running", nil }
	defer func() { vmState = origVMState }()

	var buf bytes.Buffer
	if err := psRun(base, &buf); err != nil {
		t.Fatal(err)
	}

	got := loadPsState(t, base).Services["db"]
	if got.Status != serviceStatusHealthy || got.PID != 1234 {
		t.Errorf("db = %+v, want status %q pid 1234 unchanged", got, serviceStatusHealthy)
	}
}

func TestPsReconcilesTornDownSocket(t *testing.T) {
	base := t.TempDir()
	writeStateForPs(t, base, map[string]state.ServiceState{
		"db": {Status: serviceStatusRunning, PID: 1234},
	})

	origVMState := vmState
	vmState = func(string) (string, error) { return "", nil }
	defer func() { vmState = origVMState }()

	var buf bytes.Buffer
	if err := psRun(base, &buf); err != nil {
		t.Fatal(err)
	}

	got := loadPsState(t, base).Services["db"]
	if got.Status != serviceStatusStopped || got.PID != 0 {
		t.Errorf("db = %+v, want status %q pid 0", got, serviceStatusStopped)
	}
}

func TestPsReconcilesUnreachableSocketAsCrashed(t *testing.T) {
	base := t.TempDir()
	writeStateForPs(t, base, map[string]state.ServiceState{
		"db": {Status: serviceStatusRunning, PID: 1234},
	})

	origVMState := vmState
	vmState = func(string) (string, error) { return "", errors.New("connection refused") }
	defer func() { vmState = origVMState }()

	var buf bytes.Buffer
	if err := psRun(base, &buf); err != nil {
		t.Fatal(err)
	}

	got := loadPsState(t, base).Services["db"]
	if got.Status != serviceStatusCrashed || got.PID != 0 {
		t.Errorf("db = %+v, want status %q pid 0", got, serviceStatusCrashed)
	}
}

func TestPsReconcilesPausedVM(t *testing.T) {
	base := t.TempDir()
	writeStateForPs(t, base, map[string]state.ServiceState{
		"db": {Status: serviceStatusRunning, PID: 1234},
	})

	origVMState := vmState
	vmState = func(string) (string, error) { return "Paused", nil }
	defer func() { vmState = origVMState }()

	var buf bytes.Buffer
	if err := psRun(base, &buf); err != nil {
		t.Fatal(err)
	}

	got := loadPsState(t, base).Services["db"]
	if got.Status != "paused" || got.PID != 1234 {
		t.Errorf("db = %+v, want status %q pid unchanged", got, "paused")
	}
}

func TestPsSkipsAlreadyStoppedServices(t *testing.T) {
	base := t.TempDir()
	writeStateForPs(t, base, map[string]state.ServiceState{
		"db": {Status: serviceStatusStopped, PID: 0},
	})

	called := false
	origVMState := vmState
	vmState = func(string) (string, error) { called = true; return "Running", nil }
	defer func() { vmState = origVMState }()

	var buf bytes.Buffer
	if err := psRun(base, &buf); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("vmState called for a service already recorded stopped")
	}
}
