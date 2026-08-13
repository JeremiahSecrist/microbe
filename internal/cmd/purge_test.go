package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"microbe/internal/datadir"
	"microbe/internal/hostnet"
	"microbe/internal/provisiond"
	"microbe/internal/runtime"
	"microbe/internal/state"
)

// mkPurgeOpts builds purgeOptions for tests: a dry runner, a stopFn
// recorder, a sweepOrphanLinks seam recording swept names, and stdin/out.
func mkPurgeOpts(t *testing.T, stdin string, out *bytes.Buffer) (purgeOptions, *[]int, *[]string) {
	t.Helper()
	var stopped []int
	var swept []string
	origSweep := sweepOrphanLinks
	sweepOrphanLinks = func(_ provisiond.Ops, links []string) error {
		swept = append(swept, links...)
		return nil
	}
	t.Cleanup(func() { sweepOrphanLinks = origSweep })
	return purgeOptions{
		file: writeConfig(t),
		stopFn: func(ctx context.Context, pid int, grace time.Duration) error {
			stopped = append(stopped, pid)
			return nil
		},
		out:   out,
		stdin: strings.NewReader(stdin),
	}, &stopped, &swept
}

func TestPurgeNetworksRemovesOrphans(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	// backend/frontend bridges + all taps are "known" (config + state);
	// legacyBridge and the two phantoms are leftovers to sweep.
	legacy := hostnet.BridgeName("test-net", "legacy-net")
	store := &state.Store{
		Stack: "test-net",
		Networks: map[string]state.NetworkState{
			"backend":    {CIDR: "192.168.51.0/24"},
			"frontend":   {CIDR: "192.168.50.0/24"},
			"legacy-net": {CIDR: "192.168.99.0/24"},
		},
		Services: map[string]state.ServiceState{
			"db":  {IP: map[string]string{"backend": "192.168.51.2"}},
			"web": {IP: map[string]string{"backend": "192.168.51.3", "frontend": "192.168.50.3"}},
		},
		Provisioned: []string{"br-ancient", legacy, "mvc-dead"},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts, _, swept := mkPurgeOpts(t, "y\n", &out)
	if err := purgeNetworks(opts); err != nil {
		t.Fatal(err)
	}
	sort.Slice(*swept, func(i, j int) bool { return (*swept)[i] < (*swept)[j] })
	want := []string{legacy, "br-ancient", "mvc-dead"}
	sort.Strings(want)
	if !reflect.DeepEqual(*swept, want) {
		t.Errorf("swept = %v, want %v", *swept, want)
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Provisioned) != 0 {
		t.Errorf("Provisioned = %v, want empty (orphans purged, known kept)", got.Provisioned)
	}
}

func TestPurgeNetworksHostWide(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base

	writeStore := func(name string, provisioned []string) {
		t.Helper()
		s := &state.Store{
			Stack:       name,
			Networks:    map[string]state.NetworkState{"old-net": {CIDR: "10.0.0.0/24"}},
			Provisioned: provisioned,
		}
		if err := s.Save(filepath.Join(base, name, "state.json")); err != nil {
			t.Fatal(err)
		}
	}
	legacy := hostnet.BridgeName("stack-a", "old-net")
	writeStore("stack-a", []string{legacy, "mvc-ghost"})
	writeStore("stack-b", nil)

	var out bytes.Buffer
	opts, _, swept := mkPurgeOpts(t, "y\n", &out)
	if err := purgeNetworksHostWide(opts); err != nil {
		t.Fatal(err)
	}
	sort.Slice(*swept, func(i, j int) bool { return (*swept)[i] < (*swept)[j] })
	want := []string{legacy, "mvc-ghost", hostnet.BridgeName("stack-b", "old-net")}
	sort.Strings(want)
	if !reflect.DeepEqual(*swept, want) {
		t.Errorf("host-wide swept = %v, want %v", *swept, want)
	}
	if _, err := os.Stat(filepath.Join(base, "stack-b", "state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeNetworksKeepsLiveVMDevices(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	// db is still up (PID recorded) on state-only network "wanted":
	// that bridge+tap must survive even though no compose file names it.
	store := &state.Store{
		Stack:    "test-net",
		Networks: map[string]state.NetworkState{"wanted": {CIDR: "10.9.0.0/24"}},
		Services: map[string]state.ServiceState{"db": {PID: 9001, IP: map[string]string{"wanted": "10.9.0.2"}}},
		Provisioned: []string{
			hostnet.BridgeName("test-net", "wanted"),
			"mvc-dead",
		},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts, _, swept := mkPurgeOpts(t, "y\n", &out)
	origVMState := vmState
	vmState = func(string) (string, error) { return "", nil }
	defer func() { vmState = origVMState }()
	// No compose file exists; sweep as host-wide would (state alone).
	if err := sweepOrphanedNetworkDevices(opts, store, "test-net", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*swept, []string{"mvc-dead"}) {
		t.Errorf("swept = %v, want [mvc-dead] (live VM bridge/tap preserved)", *swept)
	}
}

func TestPurgeVMsStopsRecorded(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	store := &state.Store{
		Stack: "test-net",
		Services: map[string]state.ServiceState{
			"db":  {Status: "running", PID: 1000, VirtiofsdPID: 2000},
			"web": {Status: "running", PID: 3000},
		},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "runs", "db"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts, stopped, _ := mkPurgeOpts(t, "y\n", &out)
	opts.ops = &fakeOps{}
	if err := purgeVMs(opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*stopped, []int{1000, 2000, 3000}) {
		t.Errorf("stopped pids = %v, want [1000 2000 3000]", *stopped)
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, svc := range got.Services {
		if svc.PID != 0 || svc.VirtiofsdPID != 0 || svc.Status != serviceStatusStopped {
			t.Errorf("service %s after purge = %+v, want stopped", name, svc)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "runs", "db")); !os.IsNotExist(err) {
		t.Errorf("run dir not removed for db: %v", err)
	}
}

func TestPurgeVMsFindsUntrackedSocketVM(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	store := &state.Store{
		Stack:    "test-net",
		Services: map[string]state.ServiceState{"db": {Status: "running", PID: 0}},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts, stopped, _ := mkPurgeOpts(t, "y\n", &out)
	origVMState, origPids := vmState, pidsForAPISocket
	vmState = func(string) (string, error) { return "Running", nil }
	pidsForAPISocket = func(string) []int { return []int{4242} }
	defer func() { vmState, pidsForAPISocket = origVMState, origPids }()
	if err := purgeVMs(opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*stopped, []int{4242}) {
		t.Errorf("stopped pids = %v, want [4242] (unrecorded VMM found via api socket)", *stopped)
	}
	if !strings.Contains(out.String(), "unrecorded") {
		t.Errorf("output = %q, want an unrecorded-VM notice", out.String())
	}
}

func TestPurgeVolumesRemovesDisks(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	store := &state.Store{
		Stack: "test-net",
		Networks: map[string]state.NetworkState{
			"backend": {Allocated: map[string]string{"db": "192.168.51.2", "web": "192.168.51.3"}},
		},
		Services: map[string]state.ServiceState{
			"db":  {Volumes: []string{"db-data"}, IP: map[string]string{"backend": "192.168.51.2"}},
			"web": {IP: map[string]string{"backend": "192.168.51.3", "frontend": "192.168.50.3"}},
		},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	volPath := runtime.VolumeImagePath(dataDir, "db-data")
	if err := os.MkdirAll(filepath.Dir(volPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts, _, _ := mkPurgeOpts(t, "y\n", &out)
	opts.force = true
	if err := purgeVolumes(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(volPath); !os.IsNotExist(err) {
		t.Error("disk image not removed")
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 0 {
		t.Errorf("state services after volume purge = %v, want empty", got.Services)
	}
}

func TestPurgeVolumesDeclined(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	store := &state.Store{
		Stack:    "test-net",
		Services: map[string]state.ServiceState{"db": {Volumes: []string{"db-data"}}},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	volPath := filepath.Join(dataDir, "volumes", "db-data.qcow2")
	if err := os.MkdirAll(filepath.Dir(volPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts, _, _ := mkPurgeOpts(t, "n\n", &out)
	if err := purgeVolumes(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(volPath); err != nil {
		t.Errorf("volume removed despite declining: %v", err)
	}
	if !strings.Contains(out.String(), "aborted") {
		t.Errorf("output = %q, want aborted", out.String())
	}
}

func TestPurgeDryRunDoesNotMutate(t *testing.T) {
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	store := &state.Store{
		Stack:       "test-net",
		Services:    map[string]state.ServiceState{"db": {Volumes: []string{"db-data"}, PID: 1000}},
		Provisioned: []string{"mvc-dead"},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	volPath := filepath.Join(dataDir, "volumes", "db-data.qcow2")
	if err := os.MkdirAll(filepath.Dir(volPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts, _, _ := mkPurgeOpts(t, "y\n", &out)
	opts.dryRun = true
	opts.force = true
	opts.ops = printOps{out: &out}
	if err := purgeAll(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(volPath); err != nil {
		t.Errorf("volume removed in dry-run: %v", err)
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Services["db"].PID != 1000 {
		t.Errorf("service mutated in dry-run: %+v", got.Services["db"])
	}
	if !reflect.DeepEqual(got.Provisioned, []string{"mvc-dead"}) {
		t.Errorf("Provisioned mutated in dry-run: %v", got.Provisioned)
	}
}

func TestAPISocketProcMatch(t *testing.T) {
	if !apiSocketProcMatch("cloud-hypervisor --api-socket db.sock -n 1", "db.sock") {
		t.Error("expected match for cloud-hypervisor on db.sock")
	}
	if apiSocketProcMatch("/usr/sbin/cron", "db.sock") {
		t.Error("non-cloud-hypervisor process matched")
	}
	if apiSocketProcMatch("cloud-hypervisor --api-socket web.sock", "db.sock") {
		t.Error("different socket matched")
	}
	if apiSocketProcMatch("virtiofsd --socket-path db.sock", "db.sock") {
		t.Error("virtiofsd (no --api-socket) matched as a VMM")
	}
}
