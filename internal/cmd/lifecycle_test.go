package cmd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"microbe/internal/cmdrun"
	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/hostnet"
	"microbe/internal/lockfile"
	"microbe/internal/nix/flakegen"
	"microbe/internal/provisiond"
	"microbe/internal/state"
)

const testPrefix = "fd00:1234:5678::/64"

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
      "networks": [ { "name": "backend", "addr": "fd00:1234:5678::2" } ],
      "volumes": [ { "type": "disk", "name": "db-data", "target": "/var/lib/db", "size": "2G" } ]
    },
    "web": {
      "networks": [ { "name": "backend", "addr": "fd00:1234:5678::3" }, { "name": "frontend", "addr": "fd00:1234:5678::3" } ],
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
	lock := &lockfile.Lock{Prefix: testPrefix, Services: map[string]string{}}
	plan, err := hostnet.Plan(cfg, lock)
	if err != nil {
		t.Fatal(err)
	}
	st, err := flakegen.FromConfig(cfg, plan, testPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, st
}

// TestHasVirtiofsShareFinixAlwaysTrue proves finix services always need
// virtiofsd, even with zero declared share volumes -- unlike nixos, finix
// live-shares /nix/store via virtiofs unconditionally (see finix-base.nix's
// module comment: nixos defaults to a baked store-disk image instead, since
// renderer.nix never declares a share named exactly "/nix/store").
func TestHasVirtiofsShareFinixAlwaysTrue(t *testing.T) {
	if !hasVirtiofsShare(config.Service{OS: "finix"}) {
		t.Error("hasVirtiofsShare(finix, no volumes) = false, want true (mandatory /nix/store share)")
	}
}

func TestHasVirtiofsShareNixosNeedsDeclaredVolume(t *testing.T) {
	if hasVirtiofsShare(config.Service{OS: "nixos"}) {
		t.Error("hasVirtiofsShare(nixos, no volumes) = true, want false")
	}
}

func TestVirtiofsShareSocketsFinixIncludesStore(t *testing.T) {
	sockets := virtiofsShareSockets("db", config.Service{OS: "finix"})
	want := "db-virtiofs-nix-store.sock"
	found := false
	for _, s := range sockets {
		if s == want {
			found = true
		}
	}
	if !found {
		t.Errorf("virtiofsShareSockets(finix) = %v, want to include %q", sockets, want)
	}
}

// TestCheckPortsAvailable proves the docker-style preflight rejects a host
// port that's already bound by something else, and accepts a free one.
func TestCheckPortsAvailable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	taken := l.Addr().(*net.TCPAddr).Port

	if err := checkPortsAvailable([]hostnet.PortSpec{{HostPort: taken, GuestIP: "fd00::1", GuestPort: 80}}); err == nil {
		t.Error("checkPortsAvailable(taken port): want error, got nil")
	}

	l2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	free := l2.Addr().(*net.TCPAddr).Port
	l2.Close()

	if err := checkPortsAvailable([]hostnet.PortSpec{{HostPort: free, GuestIP: "fd00::1", GuestPort: 80}}); err != nil {
		t.Errorf("checkPortsAvailable(free port %d): want nil, got %v", free, err)
	}
}

// TestUpRunFailsFastOnPortConflict proves up refuses to provision anything
// when a requested host port is already bound -- docker-style "port is
// already allocated", surfaced before any bridge/tap/DNAT/VM work.
func TestUpRunFailsFastOnPortConflict(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base

	var hr hostRecorder
	origProvision, origBuild, origStart, origCheck := provisionHost, buildRunner, startService, checkPortsAvailable
	provisionHost = recordHost(&hr, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) {
		return filepath.Join(dir, "runners", svc), nil
	}
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	checkPortsAvailable = func([]hostnet.PortSpec) error {
		return errors.New("port 8080 already allocated")
	}
	defer func() {
		provisionHost, buildRunner, startService, checkPortsAvailable = origProvision, origBuild, origStart, origCheck
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	err := upRun(nil, upOptions{ops: &fakeOps{},
		file: cfgPath, runner: rec.run, out: &buf,
	})
	if err == nil || !strings.Contains(err.Error(), "already allocated") {
		t.Fatalf("upRun err = %v, want a port-already-allocated error", err)
	}
	if hr.calls != 0 {
		t.Errorf("provisionHost calls = %d, want 0 (should fail before provisioning)", hr.calls)
	}
}

// TestNetSpecsSingleBridge proves netSpecs returns exactly one NetSpec for
// any stack with at least one network attachment -- one bridge per stack
// now, not one per declared network (see hostnet.BridgeName).
func TestNetSpecsSingleBridge(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "microbe.json")
	airgapConfigJSON := `{
	  "schemaVersion": 1,
	  "name": "airgap-net",
	  "networks": {
	    "public": {},
	    "secure": { "internal": true }
	  },
	  "services": {
	    "db": { "networks": [ { "name": "secure" } ] }
	  }
	}`
	if err := os.WriteFile(p, []byte(airgapConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	_, st := loadStack(t, p)

	specs := netSpecs(st)
	if len(specs) != 1 {
		t.Fatalf("netSpecs() = %d specs, want 1 (one bridge per stack)", len(specs))
	}
}

func TestHostSpecSlices(t *testing.T) {
	cfgPath := writeConfig(t)
	cfg, st := loadStack(t, cfgPath)

	nets := netSpecs(st)
	wantNets := []hostnet.NetSpec{
		{Gateway: "fd00:1234:5678::1", Prefix: 64},
	}
	if !reflect.DeepEqual(nets, wantNets) {
		t.Errorf("netSpecs = %v, want %v", nets, wantNets)
	}

	taps := tapSpecs(cfg, st, []string{"db", "web"})
	bridge := hostnet.BridgeName("test-net")
	wantTaps := []hostnet.TapSpec{
		{Name: flakegen.TapID("test-net", "db", "backend"), Bridge: bridge, Owner: os.Getuid(), Group: os.Getgid()},
		{Name: flakegen.TapID("test-net", "web", "backend"), Bridge: bridge, Owner: os.Getuid(), Group: os.Getgid()},
		{Name: flakegen.TapID("test-net", "web", "frontend"), Bridge: bridge, Owner: os.Getuid(), Group: os.Getgid()},
	}
	if !reflect.DeepEqual(taps, wantTaps) {
		t.Errorf("tapSpecs = %v, want %v", taps, wantTaps)
	}

	// A partial selection must not touch other services' taps: re-running
	// `up db` after a failure must never recreate web's tap out from under
	// an already-running VM.
	dbOnly := tapSpecs(cfg, st, []string{"db"})
	wantDbOnly := []hostnet.TapSpec{
		{Name: flakegen.TapID("test-net", "db", "backend"), Bridge: bridge, Owner: os.Getuid(), Group: os.Getgid()},
	}
	if !reflect.DeepEqual(dbOnly, wantDbOnly) {
		t.Errorf("tapSpecs(db only) = %v, want %v", dbOnly, wantDbOnly)
	}

	ports, err := portSpecs(cfg, st, []string{"db", "web"})
	if err != nil {
		t.Fatal(err)
	}
	wantPorts := []hostnet.PortSpec{
		{HostPort: 8080, GuestIP: "fd00:1234:5678::3", GuestPort: 80},
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

	// The stack's one bridge is still used by a non-selected (still-running)
	// service: web alone must not take it down while db is still attached.
	webOnlyNets := netSpecsForTeardown(st, []string{"web"})
	if webOnlyNets != nil {
		t.Errorf("netSpecsForTeardown(web only) = %v, want nil (db still attached)", webOnlyNets)
	}
	allNets := netSpecsForTeardown(st, []string{"db", "web"})
	if !reflect.DeepEqual(allNets, wantNets) {
		t.Errorf("netSpecsForTeardown(all) = %v, want %v (nothing left using them)", allNets, wantNets)
	}
}

const rulesConfigJSON = `{
  "schemaVersion": 1,
  "name": "test-net",
  "networks": { "backend": {} },
  "services": {
    "db": { "networks": [ { "name": "backend", "addr": "fd00:1234:5678::2" } ] },
    "web": { "networks": [ { "name": "backend", "addr": "fd00:1234:5678::3" } ] }
  },
  "rules": [ { "from": "web", "to": "db", "ports": [5432] } ]
}`

func writeRulesConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "microbe.json")
	if err := os.WriteFile(p, []byte(rulesConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRuleSpecsResolvesAddresses proves ruleSpecs resolves config.Rule
// from/to service names into the stack's actual allocated addresses.
func TestRuleSpecsResolvesAddresses(t *testing.T) {
	cfgPath := writeRulesConfig(t)
	cfg, st := loadStack(t, cfgPath)

	specs := ruleSpecs(cfg, st)
	want := []hostnet.RuleSpec{
		{From: "fd00:1234:5678::3", To: "fd00:1234:5678::2", Proto: "tcp", Port: 5432},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("ruleSpecs = %v, want %v", specs, want)
	}
}

const hostAccessConfigJSON = `{
  "schemaVersion": 1,
  "name": "test-net",
  "hostAccess": false,
  "networks": { "backend": {} },
  "services": {
    "db": {
      "networks": [ { "name": "backend", "addr": "fd00:1234:5678::2" } ],
      "hostAccess": true,
      "healthcheck": { "port": 5432, "interval": "1s", "timeout": "1s", "startPeriod": "1s" }
    },
    "web": { "networks": [ { "name": "backend", "addr": "fd00:1234:5678::3" } ] }
  }
}`

func writeHostAccessConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "microbe.json")
	if err := os.WriteFile(p, []byte(hostAccessConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestHostAccessSpecs proves hostAccessSpecs resolves the OR'd
// compose-wide/per-service HostAccess fields into one HostAccessSpec per
// unlocked service's resolved address, scoped to selected.
func TestHostAccessSpecs(t *testing.T) {
	cfgPath := writeHostAccessConfig(t)
	cfg, st := loadStack(t, cfgPath)

	got := hostAccessSpecs(cfg, st, st.Names())
	want := []hostnet.HostAccessSpec{{GuestIP: "fd00:1234:5678::2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostAccessSpecs = %v, want %v", got, want)
	}

	// Scoping: excluding db from selected drops its spec even though it's
	// host-accessible.
	if got := hostAccessSpecs(cfg, st, []string{"web"}); len(got) != 0 {
		t.Errorf("hostAccessSpecs(web only) = %v, want empty", got)
	}
}

// TestHealthAccessSpecs proves healthAccessSpecs builds one spec per
// selected service with a declared healthcheck, regardless of HostAccess.
func TestHealthAccessSpecs(t *testing.T) {
	cfgPath := writeHostAccessConfig(t)
	cfg, st := loadStack(t, cfgPath)

	got := healthAccessSpecs(cfg, st, st.Names())
	want := []hostnet.HealthAccessSpec{{GuestIP: "fd00:1234:5678::2", Port: 5432}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("healthAccessSpecs = %v, want %v", got, want)
	}

	if got := healthAccessSpecs(cfg, st, []string{"web"}); len(got) != 0 {
		t.Errorf("healthAccessSpecs(web only) = %v, want empty (no healthcheck)", got)
	}
}

// TestUpRunAppliesHostAndHealthAccess proves up wires hostAccessSpecs and
// healthAccessSpecs through to ops.ApplyHostAccess/ApplyHealthAccess before
// any healthcheck probe runs.
func TestUpRunAppliesHostAndHealthAccess(t *testing.T) {
	cfgPath := writeHostAccessConfig(t)
	base := t.TempDir()
	datadir.Root = base

	origProvision, origBuild, origStart, origWaitHealthy := provisionHost, buildRunner, startService, waitHealthy
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	waitHealthy = func(string, time.Duration, time.Duration, time.Duration) bool { return true }
	defer func() {
		provisionHost, buildRunner, startService, waitHealthy = origProvision, origBuild, origStart, origWaitHealthy
	}()

	ops := &fakeOps{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: ops, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	wantHost := []hostnet.HostAccessSpec{{GuestIP: "fd00:1234:5678::2"}}
	if !reflect.DeepEqual(ops.applyHostAccess, wantHost) {
		t.Errorf("ApplyHostAccess = %v, want %v", ops.applyHostAccess, wantHost)
	}
	wantHealth := []hostnet.HealthAccessSpec{{GuestIP: "fd00:1234:5678::2", Port: 5432}}
	if !reflect.DeepEqual(ops.applyHealthAccess, wantHealth) {
		t.Errorf("ApplyHealthAccess = %v, want %v", ops.applyHealthAccess, wantHealth)
	}
}

// TestUpRunAppliesRules proves up wires ruleSpecs() through to
// ops.ApplyRules, not just nets/taps/ports.
func TestUpRunAppliesRules(t *testing.T) {
	cfgPath := writeRulesConfig(t)
	base := t.TempDir()
	datadir.Root = base

	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	ops := &fakeOps{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: ops, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	want := []hostnet.RuleSpec{
		{From: "fd00:1234:5678::3", To: "fd00:1234:5678::2", Proto: "tcp", Port: 5432},
	}
	if !reflect.DeepEqual(ops.applyRules, want) {
		t.Errorf("ApplyRules = %v, want %v", ops.applyRules, want)
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
	ensureNetworks       int
	ensureTaps           int
	applyPorts           int
	stack                string
	prefix               string
	applyRules           []hostnet.RuleSpec
	teardownRules        []hostnet.RuleSpec
	applyHostAccess      []hostnet.HostAccessSpec
	teardownHostAccess   []hostnet.HostAccessSpec
	applyHealthAccess    []hostnet.HealthAccessSpec
	teardownHealthAccess []hostnet.HealthAccessSpec
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
func (f *fakeOps) TeardownLinks(links []string) error                          { return nil }

func (f *fakeOps) EnsurePrefix() (string, error) {
	if f.prefix == "" {
		return testPrefix, nil
	}
	return f.prefix, nil
}

func (f *fakeOps) ApplyRules(rules []hostnet.RuleSpec) error {
	f.applyRules = rules
	return nil
}
func (f *fakeOps) TeardownRules(rules []hostnet.RuleSpec) error {
	f.teardownRules = rules
	return nil
}

func (f *fakeOps) ApplyHostAccess(specs []hostnet.HostAccessSpec) error {
	f.applyHostAccess = specs
	return nil
}
func (f *fakeOps) TeardownHostAccess(specs []hostnet.HostAccessSpec) error {
	f.teardownHostAccess = specs
	return nil
}
func (f *fakeOps) ApplyHealthAccess(specs []hostnet.HealthAccessSpec) error {
	f.applyHealthAccess = specs
	return nil
}
func (f *fakeOps) TeardownHealthAccess(specs []hostnet.HealthAccessSpec) error {
	f.teardownHealthAccess = specs
	return nil
}

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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	var hr hostRecorder
	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hr, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) {
		return filepath.Join(dir, "runners", svc), nil
	}
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{},
		file: cfgPath, runner: rec.run, out: &buf,
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
	if !reflect.DeepEqual(hr.ports, []hostnet.PortSpec{{HostPort: 8080, GuestIP: "fd00:1234:5678::3", GuestPort: 80}}) {
		t.Errorf("provision ports = %v", hr.ports)
	}

	// Volume image was requested via qemu-img and formatted with mkfs.
	volPath := filepath.Join(dataDir, "volumes", "db-data.qcow2")
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

	store, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	db := store.Services["db"]
	if db.Status != "running" || db.PID != 1000 {
		t.Errorf("db state = %+v, want running pid 1000", db)
	}
	if db.Runner != filepath.Join(dataDir, "runners", "db") {
		t.Errorf("db runner = %q", db.Runner)
	}
	if db.CID != 3 || db.MACs["backend"] != "02:00:00:00:00:01" || db.Addr != "fd00:1234:5678::2" {
		t.Errorf("db state detail = %+v", db)
	}
	if !reflect.DeepEqual(db.Volumes, []string{"db-data"}) {
		t.Errorf("db volumes = %v", db.Volumes)
	}
	if !slices.Contains(store.Networks, "backend") || store.Services["web"].Addr != "fd00:1234:5678::3" {
		t.Errorf("backend network state = %+v", store.Networks)
	}
	if got := store.Ports["8080"]; got != (state.PortState{Service: "web", Guest: 80}) {
		t.Errorf("port state = %+v", got)
	}
}

// TestUpRunWarnsWhenKVMUnavailable proves a missing /dev/kvm surfaces as a
// warning instead of `up` silently starting a VM that will never boot.
func TestUpRunWarnsWhenKVMUnavailable(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base

	origProvision, origBuild, origStart, origCheckKVM := provisionHost, buildRunner, startService, checkKVMAccess
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	checkKVMAccess = func() error { return errors.New("open /dev/kvm: no such file or directory") }
	defer func() {
		provisionHost, buildRunner, startService, checkKVMAccess = origProvision, origBuild, origStart, origCheckKVM
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{},
		file: cfgPath, runner: rec.run, out: &buf,
	}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), "warning") || !strings.Contains(buf.String(), "/dev/kvm") {
		t.Errorf("output = %q, want a warning mentioning /dev/kvm", buf.String())
	}
}

const shareConfigJSON = `{
  "schemaVersion": 1,
  "name": "share-net",
  "networks": { "backend": { "subnet": "192.168.55.0/24" } },
  "services": {
    "db": {
      "networks": [ { "name": "backend", "addr": "fd00:1234:5678::2" } ],
      "volumes": [ { "name": "data", "host": "/srv/data", "target": "/data" } ]
    }
  }
}`

func writeShareConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "microbe.json")
	if err := os.WriteFile(p, []byte(shareConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUpRunStartsVirtiofsdBeforeVM proves a service with a (default,
// virtiofs) share volume gets its virtiofsd companion started and its
// socket waited on before the VM itself, and that the pid lands in
// state.json — the ordering matters because cloud-hypervisor connects to
// the virtiofsd socket at boot with no documented retry.
func TestUpRunStartsVirtiofsdBeforeVM(t *testing.T) {
	cfgPath := writeShareConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "share-net")

	var seq int
	var virtiofsdSeq, serviceSeq int
	var waitedSocket string

	origProvision, origBuild, origStart, origVirtiofsd, origWait :=
		provisionHost, buildRunner, startService, startVirtiofsd, waitForSocket
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	var capturedEnv []string
	startVirtiofsd = func(_ context.Context, _, _, _ string, env []string) (int, error) {
		capturedEnv = env
		seq++
		virtiofsdSeq = seq
		return 2000, nil
	}
	waitForSocket = func(path string, interval, timeout time.Duration) error {
		waitedSocket = path
		return nil
	}
	startService = func(context.Context, string, string, string) (int, error) {
		seq++
		serviceSeq = seq
		return 1000, nil
	}
	defer func() {
		provisionHost, buildRunner, startService, startVirtiofsd, waitForSocket =
			origProvision, origBuild, origStart, origVirtiofsd, origWait
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{}, file: cfgPath, runner: rec.run, out: &buf}); err != nil {
		t.Fatal(err)
	}

	if virtiofsdSeq == 0 {
		t.Fatal("startVirtiofsd not called for service with a share volume")
	}
	if virtiofsdSeq >= serviceSeq {
		t.Errorf("virtiofsd started at seq %d, VM at seq %d; want virtiofsd before the VM", virtiofsdSeq, serviceSeq)
	}
	wantSocket := filepath.Join(dataDir, "runs", "db", "db-virtiofs-data.sock")
	if waitedSocket != wantSocket {
		t.Errorf("waitForSocket path = %q, want %q", waitedSocket, wantSocket)
	}

	wantEnvKey := "MICROBE_SHARE_DATA="
	var foundEnvKey bool
	for _, kv := range capturedEnv {
		if strings.HasPrefix(kv, wantEnvKey) {
			foundEnvKey = true
			break
		}
	}
	if !foundEnvKey {
		t.Errorf("virtiofsd env missing %s* entry; got %v", wantEnvKey, capturedEnv)
	}

	store, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Services["db"].VirtiofsdPID; got != 2000 {
		t.Errorf("VirtiofsdPID = %d, want 2000", got)
	}
}

// TestUpRunSkipsVirtiofsdWithoutShares proves services with no virtiofs
// share never get a virtiofsd companion started — the common case
// (disk-only or no volumes) must not pay for it.
func TestUpRunSkipsVirtiofsdWithoutShares(t *testing.T) {
	cfgPath := writeConfig(t) // db (disk volume) + web (no volumes), no shares
	base := t.TempDir()
	datadir.Root = base

	origProvision, origBuild, origStart, origVirtiofsd :=
		provisionHost, buildRunner, startService, startVirtiofsd
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	startVirtiofsd = func(context.Context, string, string, string, []string) (int, error) {
		t.Fatal("startVirtiofsd called for a service with no virtiofs share")
		return 0, nil
	}
	defer func() {
		provisionHost, buildRunner, startService, startVirtiofsd =
			origProvision, origBuild, origStart, origVirtiofsd
	}()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{}, file: cfgPath, runner: (&cmdRecorder{}).run, out: &buf}); err != nil {
		t.Fatal(err)
	}
}

// TestUpRunBuildsConcurrently proves the per-service nix builds overlap
// rather than running one after another: each fake build blocks until both
// are in flight, so a sequential implementation deadlocks (caught by the
// timeout) instead of reaching maxInFlight == 2.
func TestUpRunBuildsConcurrently(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base

	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	bothInFlight := make(chan struct{})
	var once sync.Once

	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) {
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
	if err := upRun(nil, upOptions{ops: &fakeOps{},
		file: cfgPath, runner: rec.run, out: &buf,
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
      "networks": [ { "name": "backend", "addr": "fd00:1234:5678::2" } ],
      "healthcheck": { "port": 5432 }
    },
    "web": {
      "networks": [ { "name": "backend", "addr": "fd00:1234:5678::3" } ],
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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	origProvision, origBuild, origStart, origWaitHealthy := provisionHost, buildRunner, startService, waitHealthy
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	waitHealthy = func(string, time.Duration, time.Duration, time.Duration) bool { return true }
	defer func() {
		provisionHost, buildRunner, startService, waitHealthy = origProvision, origBuild, origStart, origWaitHealthy
	}()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatalf("upRun: %v", err)
	}

	store, err := state.Load(filepath.Join(dataDir, "state.json"))
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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	origProvision, origBuild, origStart, origWaitHealthy := provisionHost, buildRunner, startService, waitHealthy
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	waitHealthy = func(string, time.Duration, time.Duration, time.Duration) bool { return false }
	defer func() {
		provisionHost, buildRunner, startService, waitHealthy = origProvision, origBuild, origStart, origWaitHealthy
	}()

	var buf bytes.Buffer
	err := upRun(nil, upOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf})
	if err == nil {
		t.Fatal("upRun: want error when db never becomes healthy, got nil")
	}

	store, loadErr := state.Load(filepath.Join(dataDir, "state.json"))
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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	var stopCalls, stoppedPID int
	origProvision, origBuild, origStart, origStop, origWaitHealthy :=
		provisionHost, buildRunner, startService, stopService, waitHealthy
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
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
	if err := upRun(nil, upOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err == nil {
		t.Fatal("upRun: want error when db never becomes healthy, got nil")
	}

	if stopCalls != 1 || stoppedPID != 1234 {
		t.Errorf("stopService calls = %d, pid = %d, want 1 call with pid 1234", stopCalls, stoppedPID)
	}

	store, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if pid := store.Services["db"].PID; pid != 0 {
		t.Errorf("db PID = %d, want 0 (process stopped after healthcheck failure)", pid)
	}
}

func TestAttachShareOwnersRecordsHostOwnership(t *testing.T) {
	hostDir := t.TempDir()
	cfg := &config.Compose{
		Services: map[string]config.Service{
			"a": {
				Volumes: []config.Volume{
					{Type: "share", Name: "data", Host: hostDir, Target: "/data", Owner: "postgres"},
				},
			},
		},
	}
	st := &flakegen.Stack{Services: map[string]flakegen.Service{"a": {}}}

	if err := attachShareOwners(cfg, st); err != nil {
		t.Fatal(err)
	}
	got, ok := st.Services["a"].ShareOwners["data"]
	if !ok {
		t.Fatal("ShareOwners[data] not recorded")
	}
	if got.HostUID != os.Getuid() || got.HostGID != os.Getgid() {
		t.Errorf("ShareOwners[data] = %+v, want uid=%d gid=%d", got, os.Getuid(), os.Getgid())
	}
}

// TestAttachShareOwnersSkipsSharesWithoutOwner proves a share with no
// Owner set never gets stat'd — a nonexistent Host must not error when
// translation isn't requested.
func TestAttachShareOwnersSkipsSharesWithoutOwner(t *testing.T) {
	cfg := &config.Compose{
		Services: map[string]config.Service{
			"a": {
				Volumes: []config.Volume{
					{Type: "share", Name: "data", Host: "/does/not/exist", Target: "/data"},
				},
			},
		},
	}
	st := &flakegen.Stack{Services: map[string]flakegen.Service{"a": {}}}

	if err := attachShareOwners(cfg, st); err != nil {
		t.Fatalf("attachShareOwners with no owner set: %v", err)
	}
	if len(st.Services["a"].ShareOwners) != 0 {
		t.Errorf("ShareOwners = %+v, want empty", st.Services["a"].ShareOwners)
	}
}

// TestAttachShareHostsDefaultsMissingHostAndCreatesDir proves a share
// volume that omits host gets a docker-style managed directory under
// dataDir/volumes/<name>, that the directory actually gets created (so
// virtiofsd and attachShareOwners's stat both have somewhere to look), and
// that the default lands on both cfg (for attachShareOwners) and st (for
// generated.json/renderer.nix, which reads the raw compose file directly
// and can't see cfg's in-memory default).
func TestAttachShareHostsDefaultsMissingHostAndCreatesDir(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Compose{
		Services: map[string]config.Service{
			"a": {
				Volumes: []config.Volume{
					{Type: "share", Name: "data", Target: "/data"},
				},
			},
		},
	}
	st := &flakegen.Stack{Services: map[string]flakegen.Service{"a": {}}}

	if err := attachShareHosts(dataDir, dataDir, cfg, st); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dataDir, "volumes", "data")
	if got := cfg.Services["a"].Volumes[0].Host; got != want {
		t.Errorf("cfg volume host = %q, want %q", got, want)
	}
	if got := st.Services["a"].ShareHosts["data"]; got != want {
		t.Errorf("st ShareHosts[data] = %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Errorf("default volume dir not created: %v", err)
	}
}

// TestAttachShareHostsLeavesAbsoluteExplicitHostAlone proves an explicit
// absolute host path is preserved verbatim, and is still recorded in
// ShareHosts so renderer.nix (which reads generated.json, never Go's
// in-memory cfg) sees the same value.
func TestAttachShareHostsLeavesAbsoluteExplicitHostAlone(t *testing.T) {
	dataDir := t.TempDir()
	explicit := t.TempDir()
	cfg := &config.Compose{
		Services: map[string]config.Service{
			"a": {
				Volumes: []config.Volume{
					{Type: "share", Name: "data", Host: explicit, Target: "/data"},
				},
			},
		},
	}
	st := &flakegen.Stack{Services: map[string]flakegen.Service{"a": {}}}

	if err := attachShareHosts(dataDir, dataDir, cfg, st); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Services["a"].Volumes[0].Host; got != explicit {
		t.Errorf("cfg volume host = %q, want unchanged %q", got, explicit)
	}
	if got := st.Services["a"].ShareHosts["data"]; got != explicit {
		t.Errorf("st ShareHosts[data] = %q, want %q", got, explicit)
	}
}

// TestAttachShareHostsResolvesRelativeHostAgainstProjectDir proves a
// relative host path (e.g. "./data") is resolved against projectDir, the
// directory containing microbe.nix, rather than being passed through
// as-is (which would break once evaluated from a different CWD).
func TestAttachShareHostsResolvesRelativeHostAgainstProjectDir(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	cfg := &config.Compose{
		Services: map[string]config.Service{
			"a": {
				Volumes: []config.Volume{
					{Type: "share", Name: "data", Host: "./data", Target: "/data"},
				},
			},
		},
	}
	st := &flakegen.Stack{Services: map[string]flakegen.Service{"a": {}}}

	if err := attachShareHosts(dataDir, projectDir, cfg, st); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(projectDir, "data")
	if got := cfg.Services["a"].Volumes[0].Host; got != want {
		t.Errorf("cfg volume host = %q, want %q", got, want)
	}
	if got := st.Services["a"].ShareHosts["data"]; got != want {
		t.Errorf("st ShareHosts[data] = %q, want %q", got, want)
	}
}

func TestUpRunGeneratedNixHasAbsoluteVolumeImage(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hostRecorder{}, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) {
		return filepath.Join(dir, "runners", svc), nil
	}
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	rec := &cmdRecorder{}
	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{},
		file: cfgPath, runner: rec.run, out: &buf,
	}); err != nil {
		t.Fatal(err)
	}

	generated, err := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "generated.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "volumes", "db-data.qcow2")
	absWant, err := filepath.Abs(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), absWant) {
		t.Errorf("generated.json missing absolute volume image %q:\n%s", absWant, generated)
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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

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
		file: cfgPath, dryRun: true, runner: cmdrun.Dry(&buf), ops: printOps{out: &buf}, out: &buf,
	}); err != nil {
		t.Fatalf("dry-run up errored: %v", err)
	}
	if started {
		t.Error("dry-run started a service")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "state.json")); !os.IsNotExist(err) {
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
	datadir.Root = base

	var hr hostRecorder
	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hr, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	var buf bytes.Buffer
	ops := &fakeOps{}
	if err := upRun(nil, upOptions{file: cfgPath, ops: ops, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatalf("up errored: %v", err)
	}
	if hr.calls != 1 {
		t.Errorf("provisionHost calls = %d, want 1", hr.calls)
	}
}

func TestUpRunNoProvision(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base

	var hr hostRecorder
	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = recordHost(&hr, nil, "provision")
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{}, file: cfgPath, noProvision: true, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}
	if hr.calls != 0 {
		t.Errorf("provisionHost called %d times despite --no-provision", hr.calls)
	}
}

func TestBuildStoreAppliesHealthStatuses(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	statuses := map[string]string{"db": serviceStatusHealthy}
	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, statuses, filepath.Join(dataDir, "runners"), nil)

	if got := store.Services["db"].Status; got != serviceStatusHealthy {
		t.Errorf("db status = %q, want %q", got, serviceStatusHealthy)
	}
	if got := store.Services["web"].Status; got != serviceStatusRunning {
		t.Errorf("web status = %q, want %q (no override)", got, serviceStatusRunning)
	}
}

const testConfigDBOnlyJSON = `{
  "schemaVersion": 1,
  "name": "test-net",
  "networks": {
    "backend": { "subnet": "192.168.51.0/24" }
  },
  "services": {
    "db": {
      "networks": [ { "name": "backend", "addr": "fd00:1234:5678::2" } ],
      "volumes": [ { "type": "disk", "name": "db-data", "target": "/var/lib/db", "size": "2G" } ]
    }
  }
}`

// TestBuildStorePreservesRemovedServiceAsStale proves that a service dropped
// from config isn't lost from state just because up rebuilt the store: down
// must still be able to find and stop it by PID (spec: down works even if
// config changed after up).
func TestBuildStorePreservesRemovedServiceAsStale(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	first := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	dbID, webID := first.Services["db"].ID, first.Services["web"].ID
	if dbID == "" || webID == "" {
		t.Fatalf("buildStore left ID empty: db=%q web=%q", dbID, webID)
	}
	if dbID == webID {
		t.Fatalf("db and web got the same ID %q", dbID)
	}

	dir := t.TempDir()
	dbOnlyPath := filepath.Join(dir, "microbe.json")
	if err := os.WriteFile(dbOnlyPath, []byte(testConfigDBOnlyJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, st2 := loadStack(t, dbOnlyPath)

	second := buildStore(cfg2, st2, map[string]int{"db": 3000}, nil, nil, filepath.Join(dataDir, "runners"), first)

	web, ok := second.Services["web"]
	if !ok {
		t.Fatal("buildStore dropped web from state after it left the config; down can no longer find it")
	}
	if !web.Stale {
		t.Error("web.Stale = false, want true (no longer in config)")
	}
	if web.PID != 2000 {
		t.Errorf("web.PID = %d, want 2000 (unchanged)", web.PID)
	}
	if web.ID != webID {
		t.Errorf("web.ID = %q, want unchanged %q", web.ID, webID)
	}

	db := second.Services["db"]
	if db.Stale {
		t.Error("db.Stale = true, want false (still in config)")
	}
	if db.ID != dbID {
		t.Errorf("db.ID = %q, want unchanged %q (identity should persist across rebuilds)", db.ID, dbID)
	}
	if db.PID != 3000 {
		t.Errorf("db.PID = %d, want 3000 (new run's pid)", db.PID)
	}
}

// TestDownRunTeardownsRules proves down wires ruleSpecs() through to
// ops.TeardownRules.
func TestDownRunTeardownsRules(t *testing.T) {
	cfgPath := writeRulesConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	origTeardown, origStop := teardownHost, stopService
	teardownHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	stopService = func(context.Context, int, time.Duration) error { return nil }
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	ops := &fakeOps{}
	var buf bytes.Buffer
	if err := downRun(nil, downOptions{ops: ops, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	want := []hostnet.RuleSpec{
		{From: "fd00:1234:5678::3", To: "fd00:1234:5678::2", Proto: "tcp", Port: 5432},
	}
	if !reflect.DeepEqual(ops.teardownRules, want) {
		t.Errorf("TeardownRules = %v, want %v", ops.teardownRules, want)
	}
}

func TestDownRunOrderingAndState(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
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
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
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

	got, err := state.Load(filepath.Join(dataDir, "state.json"))
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

// TestDownRunStopsVirtiofsd proves down stops a service's recorded
// virtiofsd companion alongside its VM.
func TestDownRunStopsVirtiofsd(t *testing.T) {
	cfgPath := writeShareConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "share-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000}, map[string]int{"db": 2000}, nil, filepath.Join(dataDir, "runners"), nil)
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var stopped []int
	origTeardown, origStop := teardownHost, stopService
	teardownHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	stopService = func(_ context.Context, pid int, _ time.Duration) error {
		stopped = append(stopped, pid)
		return nil
	}
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(stopped, []int{1000, 2000}) {
		t.Errorf("stopped pids = %v, want [1000 2000] (VM then virtiofsd)", stopped)
	}

	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if svc := got.Services["db"]; svc.VirtiofsdPID != 0 {
		t.Errorf("VirtiofsdPID after down = %d, want 0", svc.VirtiofsdPID)
	}
}

func TestDownRunRemoveVolumes(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
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

	var hr hostRecorder
	origTeardown, origStop := teardownHost, stopService
	teardownHost = recordHost(&hr, nil, "teardown")
	stopService = func(context.Context, int, time.Duration) error { return nil }
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, removeVolumes: true, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(volPath); !os.IsNotExist(err) {
		t.Error("volume file not removed")
	}
	if hr.calls != 1 {
		t.Error("teardownHost not called with --remove-volumes")
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 0 {
		t.Errorf("state services = %v, want empty", got.Services)
	}
}

// TestDownRunRemoveVolumesClearsStaleServiceVolumes proves that --remove-volumes
// still finds and deletes a Stale service's disk image. buildStore/downRun
// used to delete Stale entries from store.Services before the
// --remove-volumes pass looked up their Volumes, so the volume file for a
// removed-from-config service silently survived down --remove-volumes.
func TestDownRunRemoveVolumesClearsStaleServiceVolumes(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	dbState := store.Services["db"]
	dbState.Stale = true
	store.Services["db"] = dbState
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

	origTeardown, origStop := teardownHost, stopService
	teardownHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	stopService = func(context.Context, int, time.Duration) error { return nil }
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, removeVolumes: true, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(volPath); !os.IsNotExist(err) {
		t.Error("stale service's volume file not removed by --remove-volumes")
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Services["db"]; ok {
		t.Error("stale db still present in state after down --remove-volumes")
	}
}

// TestDownRunStopsAndClearsStaleService proves that a service state carried
// forward as Stale (because it left the config, see
// TestBuildStorePreservesRemovedServiceAsStale) is actually stopped by down
// and then removed from state entirely, even without --remove-volumes.
func TestDownRunStopsAndClearsStaleService(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	webState := store.Services["web"]
	webState.Stale = true
	store.Services["web"] = webState
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var stopped []int
	origTeardown, origStop := teardownHost, stopService
	teardownHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	stopService = func(_ context.Context, pid int, _ time.Duration) error {
		stopped = append(stopped, pid)
		return nil
	}
	defer func() { teardownHost, stopService = origTeardown, origStop }()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(stopped, 2000) {
		t.Errorf("stopped pids = %v, want to include 2000 (web's stale pid)", stopped)
	}
	if !strings.Contains(buf.String(), "removed from config") {
		t.Errorf("down output = %q, want a note that web was removed from config", buf.String())
	}

	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Services["web"]; ok {
		t.Error("web still present in state after down; stale entries should be cleared once stopped")
	}
}

// TestDownRunCleansRunDir guards against the stale-socket collision this
// session hit firsthand: a torn-down VM's .sock/.sock.lock survived under
// .microbe/runs/<svc>/ and collided with the next `up`. down must remove the
// run dir once the service is stopped.
func TestDownRunCleansRunDir(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"db", "web"} {
		runDir := filepath.Join(dataDir, "runs", svc)
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
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{"db", "web"} {
		if _, err := os.Stat(filepath.Join(dataDir, "runs", svc)); !os.IsNotExist(err) {
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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
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
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "db") || !strings.Contains(buf.String(), "untracked") {
		t.Errorf("output = %q, want a warning naming db as an untracked live VM", buf.String())
	}
}

// TestDownRunSweepsOrphanedLinks is the red-green gate for the orphan sweep:
// devices microbe created for a network/service that the current config no
// longer names (here: a legacy network and two recorded-but-now-unknown
// names) must be deleted by exact name through a teardown-links call, and the
// surviving state must no longer record them.
func tapNames(cfg *config.Compose, st *flakegen.Stack, names []string) []string {
	var out []string
	for _, tap := range tapSpecs(cfg, st, names) {
		out = append(out, tap.Name)
	}
	return out
}

func TestDownRunSweepsOrphanedLinks(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")

	// Pre-bridge-collapse (old per-network) naming format: no longer
	// producible by hostnet.BridgeName, exactly the kind of leftover an
	// orphan sweep must still catch.
	legacyBridge := "br-test-net-legacy-net"
	store := &state.Store{
		Stack:    "test-net",
		Networks: []string{"backend", "frontend", "legacy-net"},
		Services: map[string]state.ServiceState{
			"db":  {Status: "running", PID: 1000, Networks: []string{"backend"}},
			"web": {Status: "running", PID: 2000, Networks: []string{"backend", "frontend"}},
		},
		// Recorded from an even older config than the one in the store above.
		Provisioned: []string{"br-ancient", legacyBridge, "mvc-dead"},
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var swept []string
	origTeardown, origStop, origSweep := teardownHost, stopService, sweepOrphanLinks
	teardownHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	stopService = func(context.Context, int, time.Duration) error { return nil }
	sweepOrphanLinks = func(_ provisiond.Ops, links []string) error {
		swept = links
		return nil
	}
	defer func() {
		teardownHost, stopService, sweepOrphanLinks = origTeardown, origStop, origSweep
	}()

	var buf bytes.Buffer
	if err := downRun(nil, downOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	// The stale bridge (legacy net not in the config) and the two phantom
	// recorded devices are orphans. The current config's bridges/taps are
	// handled by teardownHost and must NOT appear here.
	want := []string{legacyBridge, "br-ancient", "mvc-dead"}
	sort.Strings(want)
	if !reflect.DeepEqual(swept, want) {
		t.Errorf("swept links = %v, want %v", swept, want)
	}

	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Provisioned) != 0 {
		t.Errorf("Provisioned after full down = %v, want empty", got.Provisioned)
	}
}

func TestDownRunKeepsLinksForStayingServices(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	bridge := hostnet.BridgeName("test-net")
	store := &state.Store{
		Stack:    "test-net",
		Networks: []string{"backend", "frontend"},
		Services: map[string]state.ServiceState{
			"db":  {Addr: "fd00:1234:5678::2", Networks: []string{"backend"}, Status: "running", PID: 1000},
			"web": {Addr: "fd00:1234:5678::3", Networks: []string{"backend", "frontend"}, Status: "running", PID: 2000},
		},
		Provisioned: dedupeNames(append([]string{bridge},
			tapNames(cfg, st, st.Names())...)),
	}
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var swept []string
	origTeardown, origStop, origSweep := teardownHost, stopService, sweepOrphanLinks
	teardownHost = func(_ provisiond.Ops, _ string, _ []hostnet.NetSpec, _ []hostnet.TapSpec, _ []hostnet.PortSpec) error {
		return nil
	}
	stopService = func(_ context.Context, _ int, _ time.Duration) error { return nil }
	sweepOrphanLinks = func(_ provisiond.Ops, links []string) error { swept = links; return nil }
	defer func() {
		teardownHost, stopService, sweepOrphanLinks = origTeardown, origStop, origSweep
	}()

	var buf bytes.Buffer
	// Bring only web down: db stays up, so db's backend bridge and tap must
	// survive the orphan sweep.
	if err := downRun([]string{"web"}, downOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	if len(swept) != 0 {
		t.Errorf("swept links = %v, want none (db's devices must survive)", swept)
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := dedupeNames(append([]string{bridge, flakegen.TapID("test-net", "db", "backend")},
		tapNames(cfg, st, []string{"db"})...))
	if !reflect.DeepEqual(got.Provisioned, want) {
		t.Errorf("Provisioned after partial down = %v, want %v (only db's devices)", got.Provisioned, want)
	}
}

func TestUpRunRecordsProvisioned(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	origProvision, origBuild, origStart := provisionHost, buildRunner, startService
	provisionHost = func(provisiond.Ops, string, []hostnet.NetSpec, []hostnet.TapSpec, []hostnet.PortSpec) error {
		return nil
	}
	buildRunner = func(dir, svc, outLink string, _ func(string)) (string, error) { return outLink, nil }
	startService = func(context.Context, string, string, string) (int, error) { return 1000, nil }
	defer func() {
		provisionHost, buildRunner, startService = origProvision, origBuild, origStart
	}()

	var buf bytes.Buffer
	if err := upRun(nil, upOptions{ops: &fakeOps{}, file: cfgPath, runner: cmdrun.Dry(&buf), out: &buf}); err != nil {
		t.Fatal(err)
	}

	want := provisionedDeviceNames(cfg, st, st.Names())
	store, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.Provisioned, want) {
		t.Errorf("Provisioned = %v, want %v", store.Provisioned, want)
	}
}

func TestRmRequiresConfirmation(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := rmRun(nil, rmOptions{file: cfgPath, stdin: strings.NewReader("n\n"), out: &buf}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "aborted") {
		t.Errorf("output = %q, want abort message", buf.String())
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
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

	var buf bytes.Buffer
	if err := rmRun(nil, rmOptions{file: cfgPath, force: true, out: &buf}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(volPath); !os.IsNotExist(err) {
		t.Error("disk not removed")
	}
	got, err := state.Load(filepath.Join(dataDir, "state.json"))
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
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	cfg, st := loadStack(t, cfgPath)

	store := buildStore(cfg, st, map[string]int{"db": 1000, "web": 2000}, nil, nil, filepath.Join(dataDir, "runners"), nil)
	if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}

	origVMState := vmState
	vmState = func(string) (string, error) { return "Running", nil }
	defer func() { vmState = origVMState }()

	var buf bytes.Buffer
	if err := psRun(cfgPath, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"service", "db", "web", "running", "1000", "fd00:1234:5678::2", "8080->80"} {
		if !strings.Contains(out, want) {
			t.Errorf("ps output missing %q:\n%s", want, out)
		}
	}
}
