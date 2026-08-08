package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"microbe/internal/cmdrun"
	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/nix/flakegen"
	"microbe/internal/provisiond"
	"microbe/internal/state"
)

type cmdRecorder struct {
	calls [][]string
}

func (r *cmdRecorder) run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil
}

const testConfigJSON = `{
  "schemaVersion": 1,
  "name": "test-net",
  "networks": {
    "frontend": { "subnet": "192.168.50.0/24" },
    "backend": { "subnet": "192.168.51.0/24" }
  },
  "services": {
    "db": {
      "networks": [ { "name": "backend", "ip": "192.168.51.2" } ],
      "volumes": [ { "type": "disk", "name": "db-data", "target": "/var/lib/db", "size": "2G" } ]
    },
    "web": {
      "networks": [ { "name": "backend", "ip": "192.168.51.3" }, { "name": "frontend", "ip": "192.168.50.3" } ],
      "ports": [ "8080:80" ]
    }
  }
}`

func writeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "microbe.json")
	if err := os.WriteFile(p, []byte(testConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func loadStack(t *testing.T, cfgPath string) (*config.Compose, *flakegen.Stack) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := flakegen.FromConfig(cfg, plan)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, st
}

func TestParsePort(t *testing.T) {
	host, guest, err := parsePort("8080:80")
	if err != nil || host != 8080 || guest != 80 {
		t.Errorf("parsePort(8080:80) = %d,%d,%v", host, guest, err)
	}
	for _, bad := range []string{"8080", "abc:80", "8080:0", "0:80", "8080:99999", ":80"} {
		if _, _, err := parsePort(bad); err == nil {
			t.Errorf("parsePort(%q): want error", bad)
		}
	}
}

func TestHostSpecSlices(t *testing.T) {
	cfgPath := writeConfig(t)
	cfg, st := loadStack(t, cfgPath)

	nets := netSpecs(st)
	wantNets := []hostnet.NetSpec{
		{Name: "backend", Gateway: "192.168.51.1", Prefix: 24},
		{Name: "frontend", Gateway: "192.168.50.1", Prefix: 24},
	}
	if !reflect.DeepEqual(nets, wantNets) {
		t.Errorf("netSpecs = %v, want %v", nets, wantNets)
	}

	taps := tapSpecs(cfg, st, []string{"db", "web"})
	back := hostnet.BridgeName("test-net", "backend")
	front := hostnet.BridgeName("test-net", "frontend")
	wantTaps := []hostnet.TapSpec{
		{Name: flakegen.TapID("test-net", "db", "backend"), Bridge: back, Owner: os.Getuid(), Group: os.Getgid()},
		{Name: flakegen.TapID("test-net", "web", "backend"), Bridge: back, Owner: os.Getuid(), Group: os.Getgid()},
		{Name: flakegen.TapID("test-net", "web", "frontend"), Bridge: front, Owner: os.Getuid(), Group: os.Getgid()},
	}
	if !reflect.DeepEqual(taps, wantTaps) {
		t.Errorf("tapSpecs = %v, want %v", taps, wantTaps)
	}

	// A partial selection must not touch other services' taps: re-running
	// `up db` after a failure must never recreate web's tap out from under
	// an already-running VM.
	dbOnly := tapSpecs(cfg, st, []string{"db"})
	wantDbOnly := []hostnet.TapSpec{
		{Name: flakegen.TapID("test-net", "db", "backend"), Bridge: back, Owner: os.Getuid(), Group: os.Getgid()},
	}
	if !reflect.DeepEqual(dbOnly, wantDbOnly) {
		t.Errorf("tapSpecs(db only) = %v, want %v", dbOnly, wantDbOnly)
	}

	ports, err := portSpecs(cfg, st, []string{"db", "web"})
	if err != nil {
		t.Fatal(err)
	}
	wantPorts := []hostnet.PortSpec{
		{HostPort: 8080, GuestIP: "192.168.51.3", GuestPort: 80},
	}
	if !reflect.DeepEqual(ports, wantPorts) {
		t.Errorf("portSpecs = %v, want %v", ports, wantPorts)
	}

	// Scoping to a selection without web's port must exclude it: tearing
	// down db alone must never touch web's still-live DNAT rule.
	dbOnlyPorts, err := portSpecs(cfg, st, []string{"db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dbOnlyPorts) != 0 {
		t.Errorf("portSpecs(db only) = %v, want empty (web's port excluded)", dbOnlyPorts)
	}

	// A bridge still used by a non-selected (still-running) service must
	// survive teardown: web alone must not take backend down under db.
	webOnlyNets := netSpecsForTeardown(st, []string{"web"})
	if !reflect.DeepEqual(webOnlyNets, []hostnet.NetSpec{{Name: "frontend", Gateway: "192.168.50.1", Prefix: 24}}) {
		t.Errorf("netSpecsForTeardown(web only) = %v, want only frontend (backend still used by db)", webOnlyNets)
	}
	allNets := netSpecsForTeardown(st, []string{"db", "web"})
	if !reflect.DeepEqual(allNets, wantNets) {
		t.Errorf("netSpecsForTeardown(all) = %v, want %v (nothing left using them)", allNets, wantNets)
	}
}

func TestStartOrder(t *testing.T) {
	cfg := &config.Compose{
		Services: map[string]config.Service{
			"db":  {},
			"web": {DependsOn: []string{"db"}},
		},
	}
	got, err := startOrder(cfg, []string{"web", "db"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"db", "web"}) {
		t.Errorf("startOrder = %v, want [db web]", got)
	}
}

type hostRecorder struct {
	calls int
	stack string
	nets  []hostnet.NetSpec
	taps  []hostnet.TapSpec
	ports []hostnet.PortSpec
}

// fakeOps records the daemon calls made through the provisionHost seam.
type fakeOps struct {
	ensureNetworks int
	ensureTaps     int
	applyPorts     int
	stack          string
}

func (f *fakeOps) EnsureNetworks(stack string, nets []hostnet.NetSpec) error {
	f.ensureNetworks++
	f.stack = stack
	return nil
}

func (f *fakeOps) EnsureTaps(taps []hostnet.TapSpec) error {
	f.ensureTaps++
	return nil
}

func (f *fakeOps) ApplyPorts(ports []hostnet.PortSpec) error {
	f.applyPorts++
	return nil
}

func (f *fakeOps) TeardownNetworks(stack string, nets []hostnet.NetSpec) error { return nil }
func (f *fakeOps) TeardownTaps(taps []hostnet.TapSpec) error                   { return nil }
func (f *fakeOps) TeardownPorts(ports []hostnet.PortSpec) error                { return nil }

func recordHost(hr *hostRecorder, events *[]string, tag string) func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
	return func(_ provisiond.Ops, stack string, nets []hostnet.NetSpec, taps []hostnet.TapSpec, ports []hostnet.PortSpec) error {
		hr.calls++
		hr.stack = stack
		hr.nets = nets
		hr.taps = taps
		hr.ports = ports
		if events != nil {
			*events = append(*events, tag)
		}
		return nil
	}
}

func TestUpRunProvision(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	var hr hostRecorder
	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hr, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) {
		return filepath.Join(dir, "runners", svc), nil
	}
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{
		file: cfgPath, base: base, runner: rec.run, out: &buf,
	}); err != nil {
		t.Fatal(err)
	}

	if hr.calls != 1 || hr.stack != "test-net" {
		t.Fatalf("provisionHost calls = %d (stack %q), want 1 (test-net)", hr.calls, hr.stack)
	}
	if !reflect.DeepEqual(hr.nets, netSpecs(st)) {
		t.Errorf("provision nets = %v, want %v", hr.nets, netSpecs(st))
	}
	if !reflect.DeepEqual(hr.taps, tapSpecs(cfg, st, st.Names())) {
		t.Errorf("provision taps = %v, want %v", hr.taps, tapSpecs(cfg, st, st.Names()))
	}
	if !reflect.DeepEqual(hr.ports, []hostnet.PortSpec{{HostPort: 8080, GuestIP: "192.168.51.3", GuestPort: 80}}) {
		t.Errorf("provision ports = %v", hr.ports)
	}

	// Volume image was requested via qemu-img and formatted with mkfs.
	volPath := filepath.Join(base, "volumes", "test-net", "db-data.qcow2")
	var foundCreate, foundMkfs bool
	for _, c := range rec.calls {
		if len(c) > 0 && c[0] == "qemu-img" {
			foundCreate = true
			if !reflect.DeepEqual(c, []string{"qemu-img", "create", "-f", "raw", "-o", "size=2048M", volPath}) {
				t.Errorf("qemu-img call = %v", c)
			}
		}
		if reflect.DeepEqual(c, []string{"mkfs.ext4", volPath}) {
			foundMkfs = true
		}
	}
	if !foundCreate {
		t.Errorf("no qemu-img call; runner saw %v", rec.calls)
	}
	if !foundMkfs {
		t.Errorf("no mkfs.ext4 call; runner saw %v", rec.calls)
	}

	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	db := store.Services["db"]
	if db.Status != "running" || db.PID != 1000 {
		t.Errorf("db state = %+v, want running pid 1000", db)
	}
	if db.Runner != filepath.Join(base, "runners", "db") {
		t.Errorf("db runner = %q", db.Runner)
	}
	if db.CID != 3 || db.MACs["backend"] != "02:00:00:00:00:01" || db.IP["backend"] != "192.168.51.2" {
		t.Errorf("db state detail = %+v", db)
	}
	if !reflect.DeepEqual(db.Volumes, []string{"db-data"}) {
		t.Errorf("db volumes = %v", db.Volumes)
	}
	if store.Networks["backend"].CIDR != "192.168.51.0/24" || store.Networks["backend"].Allocated["web"] != "192.168.51.3" {
		t.Errorf("backend network state = %+v", store.Networks["backend"])
	}
	if got := store.Ports["8080"]; got != (state.PortState{Service: "web", Guest: 80}) {
		t.Errorf("port state = %+v", got)
	}
}

// TestUpRunBuildsConcurrently proves the per-service nix builds overlap
// rather than running one after another: each fake build blocks until both
// are in flight, so a sequential implementation deadlocks (caught by the
// timeout) instead of reaching maxInFlight == 2.
func TestUpRunBuildsConcurrently(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()

	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	bothInFlight := make(chan struct{})
	var once sync.Once

	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string) (string, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		n := inFlight
		mu.Unlock()
		if n == 2 {
			once.Do(func() { close(bothInFlight) })
		}
		select {
		case <-bothInFlight:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return outLink, nil
	}
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{
		file: cfgPath, base: base, runner: rec.run, out: &buf,
	}); err != nil {
		t.Fatal(err)
	}

	if maxInFlight < 2 {
		t.Errorf("max concurrent builds = %d, want 2 (builds should run in parallel)", maxInFlight)
	}
}

const healthcheckConfigJSON = `{
  "schemaVersion": 1,
  "name": "test-net",
  "networks": { "backend": { "subnet": "192.168.51.0/24" } },
  "services": {
    "db": {
      "networks": [ { "name": "backend", "ip": "192.168.51.2" } ],
      "healthcheck": { "port": 5432 }
    },
    "web": {
      "networks": [ { "name": "backend", "ip": "192.168.51.3" } ],
      "dependsOn": [ "db" ]
    }
  }
}`

func writeHealthcheckConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "microbe.json")
	if err := os.WriteFile(p, []byte(healthcheckConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUpRunHealthGatingHealthy(t *testing.T) {
	cfgPath := writeHealthcheckConfig(t)
	base := t.TempDir()

	origProvision, origBuild, origStart, origWaitHealthy := provisionHost, buildRunner, startService, waitHealthy
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	waitHealthy = func(string, time.Duration, time.Duration, time.Duration) bool { return true }
	defer func() {
		provisionHost, buildRunner, startService, waitHealthy = origProvision, origBuild, origStart, origWaitHealthy
	}()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{file: cfgPath, base: base, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatalf("upRun: %v", err)
	}

	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Services["db"].Status; got != serviceStatusHealthy {
		t.Errorf("db status = %q, want %q", got, serviceStatusHealthy)
	}
	if got := store.Services["web"].Status; got != serviceStatusRunning {
		t.Errorf("web status = %q, want %q", got, serviceStatusRunning)
	}
	if store.Services["web"].PID == 0 {
		t.Error("web never started despite db healthy")
	}
}

func TestUpRunHealthGatingDegraded(t *testing.T) {
	cfgPath := writeHealthcheckConfig(t)
	base := t.TempDir()

	origProvision, origBuild, origStart, origWaitHealthy := provisionHost, buildRunner, startService, waitHealthy
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	waitHealthy = func(string, time.Duration, time.Duration, time.Duration) bool { return false }
	defer func() {
		provisionHost, buildRunner, startService, waitHealthy = origProvision, origBuild, origStart, origWaitHealthy
	}()

	var buf bytes.Buffer
	err := upRun(nil, upOptions{file: cfgPath, base: base, runner: cmdrun.Dry(&buf), out: &buf})
	if err == nil {
		t.Fatal("upRun: want error when db never becomes healthy, got nil")
	}

	store, loadErr := state.Load(filepath.Join(base, "state.json"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if got := store.Services["db"].Status; got != serviceStatusDegraded {
		t.Errorf("db status = %q, want %q", got, serviceStatusDegraded)
	}
	if pid := store.Services["web"].PID; pid != 0 {
		t.Errorf("web PID = %d, want 0 (never started, db degraded)", pid)
	}
}

// TestUpRunStopsProcessOnHealthcheckFailure guards against orphaning the VM
// a failed healthcheck leaves running: a prior version left it alive, so a
// second `up` would start a competing instance and collide over the same
// microvm API socket/tap (observed corrupting the tap device entirely).
func TestUpRunStopsProcessOnHealthcheckFailure(t *testing.T) {
	cfgPath := writeHealthcheckConfig(t)
	base := t.TempDir()

	var stopCalls, stoppedPID int
	origProvision, origBuild, origStart, origStop, origWaitHealthy :=
		provisionHost, buildRunner, startService, stopService, waitHealthy
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1234, nil }
	waitHealthy = func(string, time.Duration, time.Duration, time.Duration) bool { return false }
	stopService = func(_ context.Context, pid int, _ time.Duration) error {
		stopCalls++
		stoppedPID = pid
		return nil
	}
	defer func() {
		provisionHost, buildRunner, startService, stopService, waitHealthy =
			origProvision, origBuild, origStart, origStop, origWaitHealthy
	}()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{file: cfgPath, base: base, runner: cmdrun.Dry(&buf), out: &buf}); err == nil {
		t.Fatal("upRun: want error when db never becomes healthy, got nil")
	}

	if stopCalls != 1 || stoppedPID != 1234 {
		t.Errorf("stopService calls = %d, pid = %d, want 1 call with pid 1234", stopCalls, stoppedPID)
	}

	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if pid := store.Services["db"].PID; pid != 0 {
		t.Errorf("db PID = %d, want 0 (process stopped after healthcheck failure)", pid)
	}
}

func TestUpRunGeneratedNixHasAbsoluteVolumeImage(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()

	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) {
		return filepath.Join(dir, "runners", svc), nil
	}
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{
		file: cfgPath, base: base, runner: rec.run, out: &buf,
	}); err != nil {
		t.Fatal(err)
	}

	generated, err := os.ReadFile(filepath.Join(base, "generated.nix"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "volumes", "test-net", "db-data.qcow2")
	absWant, err := filepath.Abs(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), absWant) {
		t.Errorf("generated.nix missing absolute volume image %q:\n%s", absWant, generated)
	}
}

func TestUpRunGeneratesSSHKeyAndInjectsPublicKey(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()

	origProvision, origBuild, origStart, origKeypair := provisionHost, buildRunner, startService, ensureSSHKeypair
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) {
		return filepath.Join(dir, "runners", svc), nil
	}
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	var gotDir string
	ensureSSHKeypair = func(run cmdrun.Runner, dir string) (string, string, error) {
		gotDir = dir
		return filepath.Join(dir, "id_ed25519"), "ssh-ed25519 AAAAfake microbe", nil
	}
	defer func() {
		provisionHost, buildRunner, startService, ensureSSHKeypair = origProvision, origBuild, origStart, origKeypair
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{
		file: cfgPath, base: base, runner: rec.run, out: &buf,
	}); err != nil {
		t.Fatal(err)
	}

	if gotDir != filepath.Join(base, "ssh") {
		t.Errorf("ensureSSHKeypair dir = %q, want %q", gotDir, filepath.Join(base, "ssh"))
	}
	generated, err := os.ReadFile(filepath.Join(base, "generated.nix"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), `sshPublicKey = "ssh-ed25519 AAAAfake microbe";`) {
		t.Errorf("generated.nix missing sshPublicKey:\n%s", generated)
	}
}

func TestUpRunDryRunSkipsSSHKeypair(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()

	origProvision, origStart, origKeypair := provisionHost, startService, ensureSSHKeypair
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	startService = func(context.Context, string, string, string) (int, error) { return 0, nil }
	called := false
	ensureSSHKeypair = func(run cmdrun.Runner, dir string) (string, string, error) {
		called = true
		return "", "", nil
	}
	defer func() { provisionHost, startService, ensureSSHKeypair = origProvision, origStart, origKeypair }()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{
		file: cfgPath, base: base, dryRun: true, runner: cmdrun.Dry(&buf), ops: printOps{out: &buf}, out: &buf,
	}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("dry-run invoked ensureSSHKeypair")
	}
}

func TestProvisionHostSeamForwardsToOps(t *testing.T) {
	cfg, st := loadStack(t, writeConfig(t))
	nets := netSpecs(st)
	taps := tapSpecs(cfg, st, st.Names())
	ports, err := portSpecs(cfg, st, st.Names())
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeOps{}
	if err := provisionHost(ops, st.Name, nets, taps, ports); err != nil {
		t.Fatal(err)
	}
	if ops.ensureNetworks != 1 || ops.ensureTaps != 1 || ops.applyPorts != 1 {
		t.Errorf("ops calls = %d/%d/%d, want 1/1/1", ops.ensureNetworks, ops.ensureTaps, ops.applyPorts)
	}
	if ops.stack != "test-net" {
		t.Errorf("ops stack = %q, want test-net", ops.stack)
	}
}

func TestUpRunDryRunNoRootNoStart(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()

	var hr hostRecorder
	started := false
	origProvision, origStart := provisionHost, startService
	provisionHost = recordHost(&hr, nil, "provision")
	startService = func(context.Context, string, string, string) (int, error) {
		started = true
		return 0, nil
	}
	defer func() { provisionHost, startService = origProvision, origStart }()

	var buf bytes.Buffer
	// dry-run must not require a daemon; printOps prints the intended actions.
	if err := upRun(nil, upOptions{
		file: cfgPath, base: base, dryRun: true, runner: cmdrun.Dry(&buf), ops: printOps{out: &buf}, out: &buf,
	}); err != nil {
		t.Fatalf("dry-run up errored: %v", err)
	}
	if started {
		t.Error("dry-run started a service")
	}
	if _, err := os.Stat(filepath.Join(base, "state.json")); !os.IsNotExist(err) {
		t.Error("dry-run wrote state.json")
	}
	if hr.calls != 1 {
		t.Errorf("dry-run provisionHost calls = %d, want 1", hr.calls)
	}
	if !strings.Contains(buf.String(), "nix build") {
		t.Errorf("dry-run output missing build line: %q", buf.String())
	}
}

func TestUpRunProvisionsViaDaemon(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()

	var hr hostRecorder
	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hr, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	var buf bytes.Buffer
	ops := &fakeOps{}
	if err := upRun(nil, upOptions{file: cfgPath, base: base, ops: ops, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatalf("up errored: %v", err)
	}
	if hr.calls != 1 {
		t.Errorf("provisionHost calls = %d, want 1", hr.calls)
	}
}

func TestUpRunNoProvision(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()

	var hr hostRecorder
	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hr, nil, "provision")
	buildRunner = func(dir, svc, outLink string) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{file: cfgPath, base: base, noProvision: true, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}
	if hr.calls != 0 {
		t.Errorf("provisionHost called %d times despite --no-provision", hr.calls)
	}
}

func TestBuildStoreAppliesHealthStatuses(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	statuses := map[string]string{"db": serviceStatusHealthy}
	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, statuses, filepath.Join(base, "runners"))

	if got := store.Services["db"].Status; got != serviceStatusHealthy {
		t.Errorf("db status = %q, want %q", got, serviceStatusHealthy)
	}
	if got := store.Services["web"].Status; got != serviceStatusRunning {
		t.Errorf("web status = %q, want %q (no override)", got, serviceStatusRunning)
	}
}

func TestDownRunOrderingAndState(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, filepath.Join(base, "runners"))
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}

	var hr hostRecorder
	var events []string
	var stopped []int
	origTeardown, origStop := teardownHost, stopService
	teardownHost = recordHost(&hr, &events, "teardown")
	stopService = func(_ context.Context, pid int, _ time.Duration) error {
		stopped = append(stopped, pid)
		events = append(events, "stop")
		return nil
	}
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{file: cfgPath, base: base, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(stopped, []int{1000, 2000}) {
		t.Errorf("stopped pids = %v, want [1000 2000]", stopped)
	}
	if hr.calls != 1 || hr.stack != "test-net" {
		t.Fatalf("teardownHost calls = %d (stack %q), want 1", hr.calls, hr.stack)
	}
	if !reflect.DeepEqual(hr.nets, netSpecs(st)) || !reflect.DeepEqual(hr.taps, tapSpecs(cfg, st, st.Names())) {
		t.Errorf("teardown slices mismatch: nets %v taps %v", hr.nets, hr.taps)
	}
	if events[len(events)-1] != "teardown" {
		t.Errorf("teardown not last: %v", events)
	}

	got, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, svc := range got.Services {
		if svc.Status != "stopped" || svc.PID != 0 {
			t.Errorf("service %s after down = %+v, want stopped", name, svc)
		}
	}
	if got.Services["db"].Volumes == nil {
		t.Error("down dropped db volumes from state")
	}
}

func TestDownRunRemoveVolumes(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000}, nil, filepath.Join(base, "runners"))
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}
	volPath := filepath.Join(base, "volumes", "test-net", "db-data.qcow2")
	if err := os.MkdirAll(filepath.Dir(volPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	var hr hostRecorder
	origTeardown, origStop := teardownHost, stopService
	teardownHost = recordHost(&hr, nil, "teardown")
	stopService = func(context.Context, int, time.Duration) error { return nil }
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{file: cfgPath, base: base, removeVolumes: true, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(volPath); !os.IsNotExist(err) {
		t.Error("volume file not removed")
	}
	if hr.calls != 1 {
		t.Error("teardownHost not called with --remove-volumes")
	}
	got, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 0 {
		t.Errorf("state services = %v, want empty", got.Services)
	}
}

// TestDownRunCleansRunDir guards against the stale-socket collision this
// session hit firsthand: a torn-down VM's .sock/.sock.lock survived under
// .microbe/runs/<svc>/ and collided with the next `up`. down must remove the
// run dir once the service is stopped.
func TestDownRunCleansRunDir(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, filepath.Join(base, "runners"))
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"db", "web"} {
		runDir := filepath.Join(base, "runs", svc)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, svc+".sock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, svc+".sock.lock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	origTeardown, origStop := teardownHost, stopService
	teardownHost = recordHost(&hostRecorder{}, nil, "teardown")
	stopService = func(context.Context, int, time.Duration) error { return nil }
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{file: cfgPath, base: base, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{"db", "web"} {
		if _, err := os.Stat(filepath.Join(base, "runs", svc)); !os.IsNotExist(err) {
			t.Errorf("run dir for %s still exists after down (stale socket left behind)", svc)
		}
	}
}

// TestDownRunWarnsOnUntrackedLiveVM guards the case state.json lost track
// of: a service with PID 0 in state but whose cloud-hypervisor socket
// reports it's still actually running (see the buildStore-on-partial-run
// gap: a service untouched by a run's start order gets PID 0 even if a
// prior process for it is still alive). down can't recover a killable PID
// from the socket alone, so it must at least surface this instead of
// silently reporting a clean stop.
func TestDownRunWarnsOnUntrackedLiveVM(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{}, nil, filepath.Join(base, "runners"))
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}

	origTeardown, origStop, origVMState := teardownHost, stopService, vmState
	teardownHost = recordHost(&hostRecorder{}, nil, "teardown")
	stopService = func(context.Context, int, time.Duration) error {
		t.Error("stopService called with no tracked PID")
		return nil
	}
	vmState = func(string) (string, error) { return "Running", nil }
	defer func() { teardownHost, stopService, vmState = origTeardown, origStop, origVMState }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{file: cfgPath, base: base, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "db") || !strings.Contains(buf.String(), "untracked") {
		t.Errorf("output = %q, want a warning naming db as an untracked live VM", buf.String())
	}
}

func TestRmRequiresConfirmation(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, filepath.Join(base, "runners"))
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := rmRun(nil, rmOptions{base: base, stdin: strings.NewReader("n\n"), out: &buf}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "aborted") {
		t.Errorf("output = %q, want abort message", buf.String())
	}
	got, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 2 {
		t.Errorf("services after abort = %d, want 2", len(got.Services))
	}
}

func TestRmForceRemovesDisksAndState(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000}, nil, filepath.Join(base, "runners"))
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}
	volPath := filepath.Join(base, "volumes", "test-net", "db-data.qcow2")
	if err := os.MkdirAll(filepath.Dir(volPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := rmRun(nil, rmOptions{base: base, force: true, out: &buf}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(volPath); !os.IsNotExist(err) {
		t.Error("disk not removed")
	}
	got, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 0 {
		t.Errorf("services after rm -f = %v, want empty", got.Services)
	}
}

func TestPsRunPrintsTable(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, filepath.Join(base, "runners"))
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}

	origVMState := vmState
	vmState = func(string) (string, error) { return "Running", nil }
	defer func() { vmState = origVMState }()

	var buf bytes.Buffer
	if err := psRun(base, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"service", "db", "web", "running", "1000", "192.168.51.2", "8080->80"} {
		if !strings.Contains(out, want) {
			t.Errorf("ps output missing %q:\n%s", want, out)
		}
	}
}
