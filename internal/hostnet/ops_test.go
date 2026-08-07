package hostnet

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type call struct {
	name string
	args []string
}

type fakeRunner struct {
	calls  []call
	failIf func(name string, args []string) error
}

func (f *fakeRunner) Run(name string, args ...string) error {
	f.calls = append(f.calls, call{name: name, args: args})
	if f.failIf != nil {
		return f.failIf(name, args)
	}
	return nil
}

func wantCalls(t *testing.T, got []call, wants ...call) {
	t.Helper()
	if len(got) != len(wants) {
		t.Fatalf("got %d calls, want %d\ngot:  %v", len(got), len(wants), got)
	}
	for i, w := range wants {
		if !reflect.DeepEqual(got[i], w) {
			t.Errorf("call %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func assertCalls(t *testing.T, f *fakeRunner, wants ...call) {
	t.Helper()
	wantCalls(t, f.calls, wants...)
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// simulateDevices mimics the kernel: `ip link show` fails until the device
// has been created, after which it succeeds. Creation happens on `ip link
// add` (bridges) and `ip tuntap add` (taps).
func simulateDevices() func(string, []string) error {
	existing := map[string]bool{}
	return func(name string, args []string) error {
		if name == "ip" && len(args) >= 4 && args[0] == "link" && args[1] == "show" && args[2] == "dev" {
			if !existing[args[3]] {
				return errors.New("device not found")
			}
			return nil
		}
		if name == "ip" && len(args) >= 4 {
			switch {
			case args[0] == "link" && args[1] == "add":
				existing[args[2]] = true
			case args[0] == "tuntap" && args[1] == "add":
				existing[args[3]] = true
			}
		}
		return nil
	}
}

func alwaysFail(name string, args []string) error {
	return errors.New("boom")
}

// missingProbe simulates a fresh host: the `ip link show` probe fails, so
// create/add commands are issued.
func missingProbe(name string, args []string) error {
	if name == "ip" && len(args) >= 2 && args[0] == "link" && args[1] == "show" {
		return errors.New("device not found")
	}
	return nil
}

func TestBridgeName(t *testing.T) {
	if got := BridgeName("mystack", "backend"); got != "br-mystack-backend" {
		t.Errorf("BridgeName = %q, want br-mystack-backend", got)
	}
}

func TestEnsureNetworks(t *testing.T) {
	f := &fakeRunner{failIf: missingProbe}
	nets := []NetSpec{{Name: "backend", Gateway: "192.168.51.1", Prefix: 24}}
	if err := EnsureNetworks(f.Run, "mystack", nets); err != nil {
		t.Fatalf("EnsureNetworks: %v", err)
	}
	assertCalls(t, f,
		call{"ip", []string{"link", "show", "dev", "br-mystack-backend"}},
		call{"ip", []string{"link", "add", "br-mystack-backend", "type", "bridge"}},
		call{"ip", []string{"addr", "replace", "192.168.51.1/24", "dev", "br-mystack-backend"}},
		call{"ip", []string{"link", "set", "br-mystack-backend", "up"}},
	)
}

func TestEnsureNetworksIdempotent(t *testing.T) {
	f := &fakeRunner{failIf: simulateDevices()}
	nets := []NetSpec{{Name: "backend", Gateway: "192.168.51.1", Prefix: 24}}

	if err := EnsureNetworks(f.Run, "mystack", nets); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := f.calls
	if err := EnsureNetworks(f.Run, "mystack", nets); err != nil {
		t.Fatalf("second run: %v", err)
	}
	wantCalls(t, f.calls[len(first):],
		call{"ip", []string{"link", "show", "dev", "br-mystack-backend"}},
		call{"ip", []string{"addr", "replace", "192.168.51.1/24", "dev", "br-mystack-backend"}},
		call{"ip", []string{"link", "set", "br-mystack-backend", "up"}},
	)
}

func TestEnsureNetworksErrorPropagation(t *testing.T) {
	f := &fakeRunner{failIf: func(name string, args []string) error {
		if name != "ip" {
			return nil
		}
		switch {
		case len(args) >= 2 && args[0] == "link" && args[1] == "show":
			return errors.New("device not found")
		case len(args) >= 2 && args[0] == "link" && args[1] == "add":
			return errors.New("operation not permitted")
		}
		return nil
	}}
	nets := []NetSpec{{Name: "backend", Gateway: "192.168.51.1", Prefix: 24}}
	err := EnsureNetworks(f.Run, "mystack", nets)
	if err == nil {
		t.Fatal("EnsureNetworks: want error, got nil")
	}
	if !strings.Contains(err.Error(), "hostnet: create bridge br-mystack-backend") {
		t.Errorf("error = %q, want wrapped hostnet context", err)
	}
}

func TestEnsureTaps(t *testing.T) {
	f := &fakeRunner{failIf: missingProbe}
	taps := []TapSpec{{Name: "mvc-web-backend", Bridge: "br-mystack-backend"}}
	if err := EnsureTaps(f.Run, taps); err != nil {
		t.Fatalf("EnsureTaps: %v", err)
	}
	assertCalls(t, f,
		call{"ip", []string{"link", "show", "dev", "mvc-web-backend"}},
		call{"ip", []string{"tuntap", "add", "dev", "mvc-web-backend", "mode", "tap"}},
		call{"ip", []string{"link", "set", "mvc-web-backend", "master", "br-mystack-backend"}},
		call{"ip", []string{"link", "set", "mvc-web-backend", "up"}},
	)
}

func TestEnsureTapsIdempotent(t *testing.T) {
	f := &fakeRunner{failIf: simulateDevices()}
	taps := []TapSpec{{Name: "mvc-web-backend", Bridge: "br-mystack-backend"}}

	if err := EnsureTaps(f.Run, taps); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := f.calls
	if err := EnsureTaps(f.Run, taps); err != nil {
		t.Fatalf("second run: %v", err)
	}
	wantCalls(t, f.calls[len(first):],
		call{"ip", []string{"link", "show", "dev", "mvc-web-backend"}},
		call{"ip", []string{"link", "set", "mvc-web-backend", "master", "br-mystack-backend"}},
		call{"ip", []string{"link", "set", "mvc-web-backend", "up"}},
	)
}

func TestApplyPortsRulePresent(t *testing.T) {
	f := &fakeRunner{}
	ports := []PortSpec{{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 80}}
	if err := ApplyPorts(f.Run, ports); err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	assertCalls(t, f,
		call{"iptables", []string{"-t", "nat", "-C", "PREROUTING", "-p", "tcp", "--dport", "8080", "-j", "DNAT", "--to-destination", "192.168.51.2:80"}},
	)
}

func TestApplyPortsInstallsMissingRule(t *testing.T) {
	f := &fakeRunner{failIf: func(name string, args []string) error {
		if name == "iptables" && hasArg(args, "-C") {
			return errors.New("rule not found")
		}
		return nil
	}}
	ports := []PortSpec{{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 80}}
	if err := ApplyPorts(f.Run, ports); err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	assertCalls(t, f,
		call{"iptables", []string{"-t", "nat", "-C", "PREROUTING", "-p", "tcp", "--dport", "8080", "-j", "DNAT", "--to-destination", "192.168.51.2:80"}},
		call{"iptables", []string{"-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", "8080", "-j", "DNAT", "--to-destination", "192.168.51.2:80"}},
	)
}

func TestTeardownNetworksBestEffort(t *testing.T) {
	f := &fakeRunner{failIf: alwaysFail}
	nets := []NetSpec{
		{Name: "frontend", Gateway: "192.168.50.1", Prefix: 24},
		{Name: "backend", Gateway: "192.168.51.1", Prefix: 24},
	}
	if err := TeardownNetworks(f.Run, "mystack", nets); err != nil {
		t.Fatalf("TeardownNetworks: %v", err)
	}
	assertCalls(t, f,
		call{"ip", []string{"link", "del", "br-mystack-frontend"}},
		call{"ip", []string{"link", "del", "br-mystack-backend"}},
	)
}

func TestTeardownTapsBestEffort(t *testing.T) {
	f := &fakeRunner{failIf: alwaysFail}
	taps := []TapSpec{{Name: "mvc-web-backend", Bridge: "br-mystack-backend"}}
	if err := TeardownTaps(f.Run, taps); err != nil {
		t.Fatalf("TeardownTaps: %v", err)
	}
	assertCalls(t, f, call{"ip", []string{"link", "del", "mvc-web-backend"}})
}

func TestTeardownPortsBestEffort(t *testing.T) {
	f := &fakeRunner{failIf: alwaysFail}
	ports := []PortSpec{{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 80}}
	if err := TeardownPorts(f.Run, ports); err != nil {
		t.Fatalf("TeardownPorts: %v", err)
	}
	assertCalls(t, f,
		call{"iptables", []string{"-t", "nat", "-D", "PREROUTING", "-p", "tcp", "--dport", "8080", "-j", "DNAT", "--to-destination", "192.168.51.2:80"}},
	)
}
