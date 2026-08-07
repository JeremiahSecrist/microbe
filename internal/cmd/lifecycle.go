package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/nix"
	"microbe/internal/nix/flakegen"
	"microbe/internal/provisiond"
	"microbe/internal/runtime"
	"microbe/internal/state"
)

// Seams over the host/process operations so tests can substitute fakes. The
// defaults are the real, daemon-backed implementations.
var (
	provisionHost = func(ops provisiond.Ops, stack string, st *flakegen.Stack, nets []hostnet.NetSpec, taps []hostnet.TapSpec, ports []hostnet.PortSpec) error {
		if err := ops.EnsureNetworks(stack, nets); err != nil {
			return err
		}
		if err := ops.EnsureTaps(taps); err != nil {
			return err
		}
		return ops.ApplyPorts(ports)
	}

	teardownHost = func(ops provisiond.Ops, stack string, st *flakegen.Stack, nets []hostnet.NetSpec, taps []hostnet.TapSpec, ports []hostnet.PortSpec) error {
		if err := ops.TeardownPorts(ports); err != nil {
			return err
		}
		if err := ops.TeardownTaps(taps); err != nil {
			return err
		}
		return ops.TeardownNetworks(stack, nets)
	}

	buildRunner = func(dir, svc, outLink string) (string, error) {
		return nix.BuildRunner(dir, svc, outLink)
	}

	startService = func(ctx context.Context, runnerPath, runDir, logPath string) (int, error) {
		return runtime.StartService(ctx, runnerPath, runDir, logPath)
	}

	stopService = func(ctx context.Context, pid int, grace time.Duration) error {
		return runtime.StopService(ctx, pid, grace)
	}
)

// printOps implements provisiond.Ops by printing the intended actions to w.
// Used by --dry-run, which never contacts the daemon.
type printOps struct {
	out io.Writer
}

func (p printOps) EnsureNetworks(stack string, nets []hostnet.NetSpec) error {
	for _, n := range nets {
		fmt.Fprintf(p.out, "ensure bridge %s %s/%d\n", hostnet.BridgeName(stack, n.Name), n.Gateway, n.Prefix)
	}
	return nil
}

func (p printOps) EnsureTaps(taps []hostnet.TapSpec) error {
	for _, t := range taps {
		fmt.Fprintf(p.out, "ensure tap %s -> %s\n", t.Name, t.Bridge)
	}
	return nil
}

func (p printOps) ApplyPorts(ports []hostnet.PortSpec) error {
	for _, pt := range ports {
		fmt.Fprintf(p.out, "dnat host %d -> %s:%d\n", pt.HostPort, pt.GuestIP, pt.GuestPort)
	}
	return nil
}

func (p printOps) TeardownNetworks(stack string, nets []hostnet.NetSpec) error {
	for _, n := range nets {
		fmt.Fprintf(p.out, "teardown bridge %s\n", hostnet.BridgeName(stack, n.Name))
	}
	return nil
}

func (p printOps) TeardownTaps(taps []hostnet.TapSpec) error {
	for _, t := range taps {
		fmt.Fprintf(p.out, "teardown tap %s\n", t.Name)
	}
	return nil
}

func (p printOps) TeardownPorts(ports []hostnet.PortSpec) error {
	for _, pt := range ports {
		fmt.Fprintf(p.out, "teardown dnat host %d -> %s:%d\n", pt.HostPort, pt.GuestIP, pt.GuestPort)
	}
	return nil
}

// parsePort parses a "host:guest" port mapping.
func parsePort(pm string) (host, guest int, err error) {
	parts := strings.Split(pm, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port mapping %q", pm)
	}
	host, err = strconv.Atoi(parts[0])
	if err != nil || host < 1 || host > 65535 {
		return 0, 0, fmt.Errorf("invalid port mapping %q", pm)
	}
	guest, err = strconv.Atoi(parts[1])
	if err != nil || guest < 1 || guest > 65535 {
		return 0, 0, fmt.Errorf("invalid port mapping %q", pm)
	}
	return host, guest, nil
}

// netSpecs derives one NetSpec per network, sorted by network name. Gateways
// are identical across services on the same network.
func netSpecs(st *flakegen.Stack) []hostnet.NetSpec {
	set := map[string]bool{}
	for _, svc := range st.Services {
		for _, n := range svc.Networks {
			set[n] = true
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []hostnet.NetSpec
	for _, n := range names {
		for _, svc := range st.Services {
			if g, ok := svc.Gateway[n]; ok {
				out = append(out, hostnet.NetSpec{Name: n, Gateway: g, Prefix: svc.Prefix[n]})
				break
			}
		}
	}
	return out
}

type svcNetPair struct{ svc, net string }

// tapSpecs derives one TapSpec per service/network attachment, sorted by
// service then network.
func tapSpecs(st *flakegen.Stack) []hostnet.TapSpec {
	var pairs []svcNetPair
	for name, svc := range st.Services {
		for _, n := range svc.Networks {
			pairs = append(pairs, svcNetPair{name, n})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].svc != pairs[j].svc {
			return pairs[i].svc < pairs[j].svc
		}
		return pairs[i].net < pairs[j].net
	})
	var out []hostnet.TapSpec
	for _, p := range pairs {
		out = append(out, hostnet.TapSpec{
			Name:   st.Services[p.svc].Taps[p.net],
			Bridge: hostnet.BridgeName(st.Name, p.net),
		})
	}
	return out
}

// portSpecs derives one PortSpec per published port, in sorted service order.
// The DNAT target is the service's primary network IP.
func portSpecs(cfg *config.Compose, st *flakegen.Stack) ([]hostnet.PortSpec, error) {
	var out []hostnet.PortSpec
	for _, name := range st.Names() {
		svc := st.Services[name]
		primary := ""
		if len(svc.Networks) > 0 {
			primary = svc.Networks[0]
		}
		for _, pm := range cfg.Services[name].Ports {
			host, guest, err := parsePort(pm)
			if err != nil {
				return nil, err
			}
			out = append(out, hostnet.PortSpec{
				HostPort:  host,
				GuestIP:   svc.IPs[primary],
				GuestPort: guest,
			})
		}
	}
	return out, nil
}

// startOrder returns the selected services with dependencies first.
func startOrder(cfg *config.Compose, selected []string) ([]string, error) {
	sel := map[string]bool{}
	for _, s := range selected {
		sel[s] = true
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var order []string
	var visit func(s string) error
	visit = func(s string) error {
		if visited[s] {
			return nil
		}
		if visiting[s] {
			return fmt.Errorf("dependsOn cycle at %s", s)
		}
		visiting[s] = true
		for _, dep := range cfg.Services[s].DependsOn {
			if sel[dep] {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		delete(visiting, s)
		visited[s] = true
		order = append(order, s)
		return nil
	}
	for _, s := range selected {
		if err := visit(s); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// buildStore assembles the persisted state from the stack and recorded PIDs.
func buildStore(cfg *config.Compose, st *flakegen.Stack, pids map[string]int, runnerDir string) *state.Store {
	s := &state.Store{
		Stack:    cfg.Name,
		Networks: map[string]state.NetworkState{},
		Services: map[string]state.ServiceState{},
		Ports:    map[string]state.PortState{},
	}
	for netName, net := range cfg.Networks {
		alloc := map[string]string{}
		for name, svc := range st.Services {
			if ip, ok := svc.IPs[netName]; ok {
				alloc[name] = ip
			}
		}
		s.Networks[netName] = state.NetworkState{CIDR: net.Subnet, Allocated: alloc}
	}
	for _, name := range st.Names() {
		svc := st.Services[name]
		var vols []string
		for _, v := range cfg.Services[name].Volumes {
			if v.Type == "disk" {
				vols = append(vols, v.Name)
			}
		}
		pid := pids[name]
		status := "stopped"
		if pid > 0 {
			status = "running"
		}
		s.Services[name] = state.ServiceState{
			IP:      svc.IPs,
			CID:     svc.CID,
			MACs:    svc.MACs,
			Volumes: vols,
			Status:  status,
			PID:     pid,
			Runner:  filepath.Join(runnerDir, name),
		}
	}
	for _, name := range st.Names() {
		for _, pm := range cfg.Services[name].Ports {
			host, guest, err := parsePort(pm)
			if err != nil {
				continue
			}
			s.Ports[strconv.Itoa(host)] = state.PortState{Service: name, Guest: guest}
		}
	}
	return s
}

// printStore renders the ps-style table.
func printStore(out io.Writer, s *state.Store) {
	if len(s.Services) == 0 {
		fmt.Fprintln(out, "no services")
		return
	}
	fmt.Fprintf(out, "%-12s %-9s %-7s %-24s %s\n", "service", "status", "pid", "ip", "ports")
	names := make([]string, 0, len(s.Services))
	for n := range s.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc := s.Services[n]
		var nets []string
		for net := range svc.IP {
			nets = append(nets, net)
		}
		sort.Strings(nets)
		ips := make([]string, 0, len(nets))
		for _, net := range nets {
			ips = append(ips, svc.IP[net])
		}
		var ports []string
		for hp, p := range s.Ports {
			if p.Service == n {
				ports = append(ports, fmt.Sprintf("%s->%d", hp, p.Guest))
			}
		}
		sort.Strings(ports)
		fmt.Fprintf(out, "%-12s %-9s %-7d %-24s %s\n", n, svc.Status, svc.PID, strings.Join(ips, " "), strings.Join(ports, " "))
	}
}
