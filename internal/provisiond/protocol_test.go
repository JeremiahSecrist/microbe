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
	ensureNets   []hostnet.NetSpec
	ensureTaps   []hostnet.TapSpec
	applyPorts   []hostnet.PortSpec
	teardownNets []hostnet.NetSpec
	teardownTaps []hostnet.TapSpec
	teardownPts  []hostnet.PortSpec
	stack        string
	err          error
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
	nets := []hostnet.NetSpec{{Name: "backend", Gateway: "192.168.51.1", Prefix: 24}}
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
	ports := []hostnet.PortSpec{{HostPort: 8080, GuestIP: "192.168.51.3", GuestPort: 80}}
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
	nets := []hostnet.NetSpec{{Name: "frontend", Gateway: "192.168.50.1", Prefix: 24}}
	taps := []hostnet.TapSpec{{Name: "mvc-web", Bridge: "br-x"}}
	ports := []hostnet.PortSpec{{HostPort: 8080, GuestIP: "1.2.3.4", GuestPort: 80}}

	cases := []struct {
		method  Method
		request Request
	}{
		{MethodTeardownNetworks, Request{Method: MethodTeardownNetworks, Stack: "s", Nets: nets}},
		{MethodTeardownTaps, Request{Method: MethodTeardownTaps, Taps: taps}},
		{MethodTeardownPorts, Request{Method: MethodTeardownPorts, Ports: ports}},
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
