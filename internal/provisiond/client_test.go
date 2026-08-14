package provisiond

import (
	"net"
	"path/filepath"
	"reflect"
	"testing"

	"microbe/internal/hostnet"
)

// startTestServer launches a Server on a throwaway unix socket backed by a
// fakeOps, returning the socket path and the ops recorder.
func startTestServer(t *testing.T) (string, *fakeOps) {
	t.Helper()
	ops := &fakeOps{}
	path := filepath.Join(t.TempDir(), "microbe.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ln, ops)
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	return path, ops
}

func TestClientRoundTripEnsure(t *testing.T) {
	path, ops := startTestServer(t)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	nets := []hostnet.NetSpec{{Gateway: "fd00:1234:5678::1", Prefix: 64}}
	taps := []hostnet.TapSpec{{Name: "mvc-web", Bridge: "br-test-net-backend"}}
	ports := []hostnet.PortSpec{{HostPort: 8080, GuestIP: "fd00:1234:5678::3", GuestPort: 80}}

	if err := c.EnsureNetworks("test-net", nets); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureTaps(taps); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyPorts(ports); err != nil {
		t.Fatal(err)
	}

	if ops.stack != "test-net" || !reflect.DeepEqual(ops.ensureNets, nets) {
		t.Errorf("EnsureNetworks stack=%q nets=%v", ops.stack, ops.ensureNets)
	}
	if !reflect.DeepEqual(ops.ensureTaps, taps) {
		t.Errorf("EnsureTaps = %v", ops.ensureTaps)
	}
	if !reflect.DeepEqual(ops.applyPorts, ports) {
		t.Errorf("ApplyPorts = %v", ops.applyPorts)
	}
}

func TestClientRoundTripTeardown(t *testing.T) {
	path, ops := startTestServer(t)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	nets := []hostnet.NetSpec{{Gateway: "fd00:1234:5678::1", Prefix: 64}}
	taps := []hostnet.TapSpec{{Name: "mvc-web", Bridge: "br-x"}}
	ports := []hostnet.PortSpec{{HostPort: 8080, GuestIP: "fd00:1234:5678::4", GuestPort: 80}}
	links := []string{"br-stale", "mvc-stale"}

	if err := c.TeardownNetworks("s", nets); err != nil {
		t.Fatal(err)
	}
	if err := c.TeardownTaps(taps); err != nil {
		t.Fatal(err)
	}
	if err := c.TeardownPorts(ports); err != nil {
		t.Fatal(err)
	}
	if err := c.TeardownLinks(links); err != nil {
		t.Fatal(err)
	}

	if ops.stack != "s" || !reflect.DeepEqual(ops.teardownNets, nets) {
		t.Errorf("TeardownNetworks stack=%q nets=%v", ops.stack, ops.teardownNets)
	}
	if !reflect.DeepEqual(ops.teardownTaps, taps) {
		t.Errorf("TeardownTaps = %v", ops.teardownTaps)
	}
	if !reflect.DeepEqual(ops.teardownPts, ports) {
		t.Errorf("TeardownPorts = %v", ops.teardownPts)
	}
	if !reflect.DeepEqual(ops.teardownLinks, links) {
		t.Errorf("TeardownLinks = %v", ops.teardownLinks)
	}
}

func TestClientRoundTripRules(t *testing.T) {
	path, ops := startTestServer(t)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	rules := []hostnet.RuleSpec{{From: "fd00::1", To: "fd00::2", Proto: "tcp", Port: 5432}}
	if err := c.ApplyRules(rules); err != nil {
		t.Fatal(err)
	}
	if err := c.TeardownRules(rules); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ops.applyRules, rules) {
		t.Errorf("ApplyRules = %v, want %v", ops.applyRules, rules)
	}
	if !reflect.DeepEqual(ops.teardownRules, rules) {
		t.Errorf("TeardownRules = %v, want %v", ops.teardownRules, rules)
	}
}

func TestClientRoundTripHostAndHealthAccess(t *testing.T) {
	path, ops := startTestServer(t)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hostSpecs := []hostnet.HostAccessSpec{{GuestIP: "fd00::2"}}
	healthSpecs := []hostnet.HealthAccessSpec{{GuestIP: "fd00::2", Port: 5432}}

	if err := c.ApplyHostAccess(hostSpecs); err != nil {
		t.Fatal(err)
	}
	if err := c.TeardownHostAccess(hostSpecs); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyHealthAccess(healthSpecs); err != nil {
		t.Fatal(err)
	}
	if err := c.TeardownHealthAccess(healthSpecs); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(ops.applyHostAccess, hostSpecs) {
		t.Errorf("ApplyHostAccess = %v, want %v", ops.applyHostAccess, hostSpecs)
	}
	if !reflect.DeepEqual(ops.teardownHostAccess, hostSpecs) {
		t.Errorf("TeardownHostAccess = %v, want %v", ops.teardownHostAccess, hostSpecs)
	}
	if !reflect.DeepEqual(ops.applyHealthAccess, healthSpecs) {
		t.Errorf("ApplyHealthAccess = %v, want %v", ops.applyHealthAccess, healthSpecs)
	}
	if !reflect.DeepEqual(ops.teardownHealthAccess, healthSpecs) {
		t.Errorf("TeardownHealthAccess = %v, want %v", ops.teardownHealthAccess, healthSpecs)
	}
}

func TestClientPropagatesDaemonError(t *testing.T) {
	path, ops := startTestServer(t)
	ops.err = &provisionErr{"operation not permitted"}
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	err = c.ApplyPorts([]hostnet.PortSpec{{HostPort: 8080, GuestIP: "fd00:1234:5678::4", GuestPort: 80}})
	if err == nil {
		t.Fatal("want error from daemon, got nil")
	}
	if !reflect.DeepEqual([]byte(err.Error()), []byte("provisiond: operation not permitted")) {
		t.Errorf("error = %q", err)
	}
}

func TestDialMissingDaemon(t *testing.T) {
	_, err := Dial(filepath.Join(t.TempDir(), "nope.sock"))
	if err == nil {
		t.Fatal("want error dialing missing daemon")
	}
}
