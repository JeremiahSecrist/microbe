package provisiond

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"microbe/internal/hostnet"
)

// fakeOps records the arguments each Ops method receives and returns err.
type fakeOps struct {
	ensureNets           []hostnet.NetSpec
	ensureTaps           []hostnet.TapSpec
	applyPorts           []hostnet.PortSpec
	teardownNets         []hostnet.NetSpec
	teardownTaps         []hostnet.TapSpec
	teardownPts          []hostnet.PortSpec
	teardownLinks        []string
	stack                string
	prefix               string
	applyRules           []hostnet.RuleSpec
	teardownRules        []hostnet.RuleSpec
	applyHostAccess      []hostnet.HostAccessSpec
	teardownHostAccess   []hostnet.HostAccessSpec
	applyHealthAccess    []hostnet.HealthAccessSpec
	teardownHealthAccess []hostnet.HealthAccessSpec
	err                  error
}

func (f *fakeOps) EnsureNetworks(stack string, nets []hostnet.NetSpec) error {
	f.stack, f.ensureNets = stack, nets
	return f.err
}
func (f *fakeOps) EnsureTaps(taps []hostnet.TapSpec) error {
	f.ensureTaps = taps
	return f.err
}
func (f *fakeOps) ApplyPorts(ports []hostnet.PortSpec) error {
	f.applyPorts = ports
	return f.err
}
func (f *fakeOps) TeardownNetworks(stack string, nets []hostnet.NetSpec) error {
	f.stack, f.teardownNets = stack, nets
	return f.err
}
func (f *fakeOps) TeardownTaps(taps []hostnet.TapSpec) error {
	f.teardownTaps = taps
	return f.err
}
func (f *fakeOps) TeardownPorts(ports []hostnet.PortSpec) error {
	f.teardownPts = ports
	return f.err
}
func (f *fakeOps) TeardownLinks(links []string) error {
	f.teardownLinks = links
	return f.err
}
func (f *fakeOps) EnsurePrefix() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.prefix, nil
}
func (f *fakeOps) ApplyRules(rules []hostnet.RuleSpec) error {
	f.applyRules = rules
	return f.err
}
func (f *fakeOps) TeardownRules(rules []hostnet.RuleSpec) error {
	f.teardownRules = rules
	return f.err
}
func (f *fakeOps) ApplyHostAccess(specs []hostnet.HostAccessSpec) error {
	f.applyHostAccess = specs
	return f.err
}
func (f *fakeOps) TeardownHostAccess(specs []hostnet.HostAccessSpec) error {
	f.teardownHostAccess = specs
	return f.err
}
func (f *fakeOps) ApplyHealthAccess(specs []hostnet.HealthAccessSpec) error {
	f.applyHealthAccess = specs
	return f.err
}
func (f *fakeOps) TeardownHealthAccess(specs []hostnet.HealthAccessSpec) error {
	f.teardownHealthAccess = specs
	return f.err
}

func decodeResp(t *testing.T, buf *bytes.Buffer) Response {
	t.Helper()
	var r Response
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return r
}

func TestDispatchEnsureNetworks(t *testing.T) {
	ops := &fakeOps{}
	nets := []hostnet.NetSpec{{Gateway: "fd00:1234:5678::1", Prefix: 64}}
	req := Request{Method: MethodEnsureNetworks, Stack: "test-net", Nets: nets}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, req); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if ops.stack != "test-net" || !reflect.DeepEqual(ops.ensureNets, nets) {
		t.Errorf("EnsureNetworks(%q, %v)", ops.stack, ops.ensureNets)
	}
}

func TestDispatchEnsureTaps(t *testing.T) {
	ops := &fakeOps{}
	taps := []hostnet.TapSpec{{Name: "mvc-web", Bridge: "br-test-net-backend"}}
	req := Request{Method: MethodEnsureTaps, Taps: taps}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, req); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.ensureTaps, taps) {
		t.Errorf("EnsureTaps = %v", ops.ensureTaps)
	}
}

func TestDispatchApplyPorts(t *testing.T) {
	ops := &fakeOps{}
	ports := []hostnet.PortSpec{{HostPort: 8080, GuestIP: "fd00:1234:5678::3", GuestPort: 80}}
	req := Request{Method: MethodApplyPorts, Ports: ports}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, req); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.applyPorts, ports) {
		t.Errorf("ApplyPorts = %v", ops.applyPorts)
	}
}

func TestDispatchTeardown(t *testing.T) {
	ops := &fakeOps{}
	nets := []hostnet.NetSpec{{Gateway: "fd00:1234:5678::1", Prefix: 64}}
	taps := []hostnet.TapSpec{{Name: "mvc-web", Bridge: "br-x"}}
	ports := []hostnet.PortSpec{{HostPort: 8080, GuestIP: "fd00:1234:5678::4", GuestPort: 80}}

	cases := []struct {
		method  Method
		request Request
	}{
		{MethodTeardownNetworks, Request{Method: MethodTeardownNetworks, Stack: "s", Nets: nets}},
		{MethodTeardownTaps, Request{Method: MethodTeardownTaps, Taps: taps}},
		{MethodTeardownPorts, Request{Method: MethodTeardownPorts, Ports: ports}},
		{MethodTeardownLinks, Request{Method: MethodTeardownLinks, Links: []string{"br-x", "mvc-y"}}},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := dispatch(&buf, ops, c.request); err != nil {
			t.Fatalf("%s: %v", c.method, err)
		}
		if resp := decodeResp(t, &buf); resp.Error != "" {
			t.Fatalf("%s: unexpected error %q", c.method, resp.Error)
		}
	}
	if !reflect.DeepEqual(ops.teardownNets, nets) || ops.stack != "s" {
		t.Errorf("TeardownNetworks stack=%q nets=%v", ops.stack, ops.teardownNets)
	}
	if !reflect.DeepEqual(ops.teardownTaps, taps) {
		t.Errorf("TeardownTaps = %v", ops.teardownTaps)
	}
	if !reflect.DeepEqual(ops.teardownPts, ports) {
		t.Errorf("TeardownPorts = %v", ops.teardownPts)
	}
	if !reflect.DeepEqual(ops.teardownLinks, []string{"br-x", "mvc-y"}) {
		t.Errorf("TeardownLinks = %v", ops.teardownLinks)
	}
}

func TestDispatchErrorPropagates(t *testing.T) {
	ops := &fakeOps{err: &provisionErr{"boom"}}
	req := Request{Method: MethodApplyPorts, Ports: []hostnet.PortSpec{{HostPort: 1}}}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, req); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error == "" || !reflect.DeepEqual([]byte(resp.Error), []byte("boom")) {
		t.Fatalf("error response = %q, want boom", resp.Error)
	}
}

func TestDispatchEnsurePrefix(t *testing.T) {
	ops := &fakeOps{prefix: "fd7a:3c9e:1122::/64"}
	req := Request{Method: MethodEnsurePrefix}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, req); err != nil {
		t.Fatal(err)
	}
	resp := decodeResp(t, &buf)
	if resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if resp.Prefix != "fd7a:3c9e:1122::/64" {
		t.Errorf("Prefix = %q, want fd7a:3c9e:1122::/64", resp.Prefix)
	}
}

func TestDispatchApplyAndTeardownRules(t *testing.T) {
	ops := &fakeOps{}
	rules := []hostnet.RuleSpec{{From: "fd00::1", To: "fd00::2", Proto: "tcp", Port: 5432}}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, Request{Method: MethodApplyRules, Rules: rules}); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.applyRules, rules) {
		t.Errorf("ApplyRules = %v, want %v", ops.applyRules, rules)
	}

	buf.Reset()
	if err := dispatch(&buf, ops, Request{Method: MethodTeardownRules, Rules: rules}); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.teardownRules, rules) {
		t.Errorf("TeardownRules = %v, want %v", ops.teardownRules, rules)
	}
}

func TestDispatchApplyAndTeardownHostAccess(t *testing.T) {
	ops := &fakeOps{}
	specs := []hostnet.HostAccessSpec{{GuestIP: "fd00::2"}}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, Request{Method: MethodApplyHostAccess, HostAccess: specs}); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.applyHostAccess, specs) {
		t.Errorf("ApplyHostAccess = %v, want %v", ops.applyHostAccess, specs)
	}

	buf.Reset()
	if err := dispatch(&buf, ops, Request{Method: MethodTeardownHostAccess, HostAccess: specs}); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.teardownHostAccess, specs) {
		t.Errorf("TeardownHostAccess = %v, want %v", ops.teardownHostAccess, specs)
	}
}

func TestDispatchApplyAndTeardownHealthAccess(t *testing.T) {
	ops := &fakeOps{}
	specs := []hostnet.HealthAccessSpec{{GuestIP: "fd00::2", Port: 5432}}

	var buf bytes.Buffer
	if err := dispatch(&buf, ops, Request{Method: MethodApplyHealthAccess, HealthAccess: specs}); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.applyHealthAccess, specs) {
		t.Errorf("ApplyHealthAccess = %v, want %v", ops.applyHealthAccess, specs)
	}

	buf.Reset()
	if err := dispatch(&buf, ops, Request{Method: MethodTeardownHealthAccess, HealthAccess: specs}); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error != "" {
		t.Fatalf("unexpected error response: %q", resp.Error)
	}
	if !reflect.DeepEqual(ops.teardownHealthAccess, specs) {
		t.Errorf("TeardownHealthAccess = %v, want %v", ops.teardownHealthAccess, specs)
	}
}

func TestDispatchUnknownMethod(t *testing.T) {
	ops := &fakeOps{}
	req := Request{Method: "bogus"}
	var buf bytes.Buffer
	if err := dispatch(&buf, ops, req); err != nil {
		t.Fatal(err)
	}
	if resp := decodeResp(t, &buf); resp.Error == "" {
		t.Fatal("unknown method: want error response")
	}
}

type provisionErr struct{ msg string }

func (e *provisionErr) Error() string { return e.msg }
