package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"microbe/internal/chapi"
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
	provisionHost = func(ops provisiond.Ops, stack string, nets []hostnet.NetSpec, taps []hostnet.TapSpec, ports []hostnet.PortSpec) error {
		if err := ops.EnsureNetworks(stack, nets); err != nil {
			return err
		}
		if err := ops.EnsureTaps(taps); err != nil {
			return err
		}
		return ops.ApplyPorts(ports)
	}

	teardownHost = func(ops provisiond.Ops, stack string, nets []hostnet.NetSpec, taps []hostnet.TapSpec, ports []hostnet.PortSpec) error {
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

	startVirtiofsd = func(ctx context.Context, runnerPath, runDir, logPath string) (int, error) {
		return runtime.StartVirtiofsd(ctx, runnerPath, runDir, logPath)
	}

	waitForSocket = func(path string, interval, timeout time.Duration) error {
		return runtime.WaitForSocket(path, interval, timeout)
	}

	stopService = func(ctx context.Context, pid int, grace time.Duration) error {
		return runtime.StopService(ctx, pid, grace)
	}

	vmState = chapi.VMState
)

// vmSocketPath is where the cloud-hypervisor --api-socket for svc lives:
// <hostname>.sock relative to the runner's CWD (microvm.nix's own
// convention, see lib/runners/cloud-hypervisor.nix upstream), and svc's
// runDir is set to exactly that CWD by startService.
func vmSocketPath(base, svc string) string {
	return filepath.Join(base, "runs", svc, svc+".sock")
}

// virtiofsdSocketWait/Interval bound how long up waits for virtiofsd's
// socket(s) to appear before starting the VM that connects to them.
const (
	virtiofsdSocketWaitInterval = 50 * time.Millisecond
	virtiofsdSocketWaitTimeout  = 5 * time.Second
)

// hasVirtiofsShare reports whether svc declares any virtiofs share, i.e.
// whether its runner build carries a bin/virtiofsd-run companion script
// (microvm.nix's microvm.binScripts.virtiofsd-run, gated by
// requiresVirtiofsd) that up must start before the VM.
func hasVirtiofsShare(svc config.Service) bool {
	for _, v := range svc.Volumes {
		if v.Type == "share" && v.Protocol == "virtiofs" {
			return true
		}
	}
	return false
}

// virtiofsShareSockets returns the socket filenames up must wait for before
// starting svc's VM, one per virtiofs share, relative to the service's
// runDir. Matches microvm.nix's own default (nixos-modules/microvm/
// options.nix): "<hostname>-virtiofs-<tag>.sock", tag = the volume name
// (see renderer.nix's `tag = v.name`).
func virtiofsShareSockets(svcName string, svc config.Service) []string {
	var out []string
	for _, v := range svc.Volumes {
		if v.Type == "share" && v.Protocol == "virtiofs" {
			out = append(out, fmt.Sprintf("%s-virtiofs-%s.sock", svcName, v.Name))
		}
	}
	return out
}

// printOps implements provisiond.Ops by printing the intended actions to out.
// Used by --dry-run, which never contacts the daemon.
type printOps struct {
	out io.Writer
}

func (p printOps) EnsureNetworks(stack string, nets []hostnet.NetSpec) error {
	for _, netSpec := range nets {
		fmt.Fprintf(p.out, "ensure bridge %s %s/%d\n", hostnet.BridgeName(stack, netSpec.Name), netSpec.Gateway, netSpec.Prefix)
	}
	return nil
}

func (p printOps) EnsureTaps(taps []hostnet.TapSpec) error {
	for _, tap := range taps {
		fmt.Fprintf(p.out, "ensure tap %s -> %s\n", tap.Name, tap.Bridge)
	}
	return nil
}

func (p printOps) ApplyPorts(ports []hostnet.PortSpec) error {
	for _, port := range ports {
		fmt.Fprintf(p.out, "dnat host %d -> %s:%d\n", port.HostPort, port.GuestIP, port.GuestPort)
	}
	return nil
}

func (p printOps) TeardownNetworks(stack string, nets []hostnet.NetSpec) error {
	for _, netSpec := range nets {
		fmt.Fprintf(p.out, "teardown bridge %s\n", hostnet.BridgeName(stack, netSpec.Name))
	}
	return nil
}

func (p printOps) TeardownTaps(taps []hostnet.TapSpec) error {
	for _, tap := range taps {
		fmt.Fprintf(p.out, "teardown tap %s\n", tap.Name)
	}
	return nil
}

func (p printOps) TeardownPorts(ports []hostnet.PortSpec) error {
	for _, port := range ports {
		fmt.Fprintf(p.out, "teardown dnat host %d -> %s:%d\n", port.HostPort, port.GuestIP, port.GuestPort)
	}
	return nil
}

// parsePort parses a "host:guest" port mapping.
func parsePort(portMapping string) (host, guest int, err error) {
	parts := strings.Split(portMapping, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port mapping %q", portMapping)
	}
	host, err = strconv.Atoi(parts[0])
	if err != nil || host < 1 || host > 65535 {
		return 0, 0, fmt.Errorf("invalid port mapping %q", portMapping)
	}
	guest, err = strconv.Atoi(parts[1])
	if err != nil || guest < 1 || guest > 65535 {
		return 0, 0, fmt.Errorf("invalid port mapping %q", portMapping)
	}
	return host, guest, nil
}

// netSpecs derives one NetSpec per network, sorted by network name. Gateways
// are identical across services on the same network.
func netSpecs(st *flakegen.Stack) []hostnet.NetSpec {
	netNameSet := map[string]bool{}
	for _, svc := range st.Services {
		for _, netName := range svc.Networks {
			netNameSet[netName] = true
		}
	}
	names := make([]string, 0, len(netNameSet))
	for netName := range netNameSet {
		names = append(names, netName)
	}
	sort.Strings(names)
	var specs []hostnet.NetSpec
	for _, netName := range names {
		for _, svc := range st.Services {
			if gateway, ok := svc.Gateway[netName]; ok {
				specs = append(specs, hostnet.NetSpec{
					Name:     netName,
					Gateway:  gateway,
					Prefix:   svc.Prefix[netName],
					Internal: st.Internal[netName],
				})
				break
			}
		}
	}
	return specs
}

// netSpecsForTeardown returns the subset of netSpecs(st) safe to actually
// tear down: a network still used by a service outside selected (i.e.
// staying up) must survive, or TeardownNetworks would delete a bridge a
// still-running service depends on out from under it.
func netSpecsForTeardown(st *flakegen.Stack, selected []string) []hostnet.NetSpec {
	isSelected := map[string]bool{}
	for _, name := range selected {
		isSelected[name] = true
	}
	inUseByOthers := map[string]bool{}
	for name, svc := range st.Services {
		if isSelected[name] {
			continue
		}
		for _, netName := range svc.Networks {
			inUseByOthers[netName] = true
		}
	}
	var specs []hostnet.NetSpec
	for _, spec := range netSpecs(st) {
		if !inUseByOthers[spec.Name] {
			specs = append(specs, spec)
		}
	}
	return specs
}

// svcNetPair identifies one service's attachment to one network.
type svcNetPair struct{ service, network string }

// tapSpecs derives one TapSpec per service/network attachment, sorted by
// service then network, for exactly the services named in selected. cfg
// supplies each service's vcpu count: TapSpec.MultiQueue must match
// microvm.nix's cloud-hypervisor.nix `tapMultiQueue = vcpu > 1` exactly, or
// cloud-hypervisor refuses to attach (see provisiond's
// tapLink/tapNeedsRecreate).
//
// Callers must scope selected to only the services actually being
// (re)provisioned this run: EnsureTaps deletes and recreates a mismatched
// tap, and doing that to a service not part of this run would yank the
// network out from under an already-running VM (observed corrupting
// web/jump while only db was being retried after a failed up).
func tapSpecs(cfg *config.Compose, st *flakegen.Stack, selected []string) []hostnet.TapSpec {
	isSelected := map[string]bool{}
	for _, name := range selected {
		isSelected[name] = true
	}
	var pairs []svcNetPair
	for name, svc := range st.Services {
		if !isSelected[name] {
			continue
		}
		for _, netName := range svc.Networks {
			pairs = append(pairs, svcNetPair{name, netName})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].service != pairs[j].service {
			return pairs[i].service < pairs[j].service
		}
		return pairs[i].network < pairs[j].network
	})
	var specs []hostnet.TapSpec
	for _, pair := range pairs {
		specs = append(specs, hostnet.TapSpec{
			Name:       st.Services[pair.service].Taps[pair.network],
			Bridge:     hostnet.BridgeName(st.Name, pair.network),
			Owner:      os.Getuid(),
			Group:      os.Getgid(),
			MultiQueue: cfg.Services[pair.service].VCPUs > 1,
		})
	}
	return specs
}

// primaryNetwork returns a service's first declared network: the one that
// gets the default route (see Stack.Networks) and is used as the address
// for DNAT targets and healthchecks. "" if the service has no networks.
func primaryNetwork(svc flakegen.Service) string {
	if len(svc.Networks) == 0 {
		return ""
	}
	return svc.Networks[0]
}

// portSpecs derives one PortSpec per published port belonging to a service
// in selected, in sorted service order. The DNAT target is the service's
// primary network IP. Callers tearing down ports must scope selected to the
// services actually being brought down: TeardownPorts deletes by exact
// match, so an unscoped call would delete a still-running service's DNAT
// rule too.
func portSpecs(cfg *config.Compose, st *flakegen.Stack, selected []string) ([]hostnet.PortSpec, error) {
	isSelected := map[string]bool{}
	for _, name := range selected {
		isSelected[name] = true
	}
	var specs []hostnet.PortSpec
	for _, name := range st.Names() {
		if !isSelected[name] {
			continue
		}
		svc := st.Services[name]
		primary := primaryNetwork(svc)
		for _, portMapping := range cfg.Services[name].Ports {
			host, guest, err := parsePort(portMapping)
			if err != nil {
				return nil, err
			}
			specs = append(specs, hostnet.PortSpec{
				HostPort:  host,
				GuestIP:   svc.IPs[primary],
				GuestPort: guest,
			})
		}
	}
	return specs, nil
}

// startOrder returns the selected services with dependencies first.
func startOrder(cfg *config.Compose, selected []string) ([]string, error) {
	isSelected := map[string]bool{}
	for _, name := range selected {
		isSelected[name] = true
	}
	// visiting/visited track a standard DFS: a name in visiting is an
	// ancestor on the current path (its presence there signals a cycle),
	// while visited marks names whose dependencies are already resolved.
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var order []string
	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("dependsOn cycle at %s", name)
		}
		visiting[name] = true
		for _, dep := range cfg.Services[name].DependsOn {
			if isSelected[dep] {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		delete(visiting, name)
		visited[name] = true
		order = append(order, name)
		return nil
	}
	for _, name := range selected {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Volume type and service status sentinels shared across the lifecycle
// commands (up/down/ps state rendering).
const (
	volumeTypeDisk        = "disk"
	serviceStatusRunning  = "running"
	serviceStatusStopped  = "stopped"
	serviceStatusHealthy  = "healthy"
	serviceStatusDegraded = "degraded"
	serviceStatusCrashed  = "crashed"
)

// buildStore assembles the persisted state from the stack and recorded PIDs.
// statuses overrides the default pid-based running/stopped computation for
// any service present in the map (e.g. healthy/degraded from a
// healthcheck); a nil map or a missing entry leaves that computation as-is.
func buildStore(cfg *config.Compose, st *flakegen.Stack, pids, virtiofsdPIDs map[string]int, statuses map[string]string, runnerDir string) *state.Store {
	store := &state.Store{
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
		store.Networks[netName] = state.NetworkState{CIDR: net.Subnet, Allocated: alloc}
	}
	for _, name := range st.Names() {
		svc := st.Services[name]
		var vols []string
		for _, vol := range cfg.Services[name].Volumes {
			if vol.Type == volumeTypeDisk {
				vols = append(vols, vol.Name)
			}
		}
		pid := pids[name]
		status := serviceStatusStopped
		if pid > 0 {
			status = serviceStatusRunning
		}
		if override, ok := statuses[name]; ok {
			status = override
		}
		store.Services[name] = state.ServiceState{
			IP:           svc.IPs,
			CID:          svc.CID,
			MACs:         svc.MACs,
			Volumes:      vols,
			Status:       status,
			PID:          pid,
			Runner:       filepath.Join(runnerDir, name),
			VirtiofsdPID: virtiofsdPIDs[name],
		}
	}
	for _, name := range st.Names() {
		for _, portMapping := range cfg.Services[name].Ports {
			host, guest, err := parsePort(portMapping)
			if err != nil {
				continue
			}
			store.Ports[strconv.Itoa(host)] = state.PortState{Service: name, Guest: guest}
		}
	}
	return store
}

// statusColor maps a service status to its ANSI color code for interactive
// output: green for up and running, red for anything degraded/crashed,
// dim gray for stopped/unknown.
func statusColor(status string) string {
	switch status {
	case serviceStatusRunning, serviceStatusHealthy:
		return "32"
	case serviceStatusDegraded, serviceStatusCrashed:
		return "31"
	default:
		return "90"
	}
}

// printStore renders the ps-style table. In interactive mode the status
// column is colorized.
func printStore(out io.Writer, store *state.Store, interactive bool) {
	if len(store.Services) == 0 {
		fmt.Fprintln(out, "no services")
		return
	}
	fmt.Fprintf(out, "%-12s %-9s %-7s %-24s %s\n", "service", "status", "pid", "ip", "ports")
	names := make([]string, 0, len(store.Services))
	for name := range store.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := store.Services[name]
		var netNames []string
		for netName := range svc.IP {
			netNames = append(netNames, netName)
		}
		sort.Strings(netNames)
		ips := make([]string, 0, len(netNames))
		for _, netName := range netNames {
			ips = append(ips, svc.IP[netName])
		}
		var ports []string
		for hostPort, portState := range store.Ports {
			if portState.Service == name {
				ports = append(ports, fmt.Sprintf("%s->%d", hostPort, portState.Guest))
			}
		}
		sort.Strings(ports)
		status := svc.Status
		if interactive {
			status = fmt.Sprintf("\x1b[%sm%s\x1b[0m", statusColor(svc.Status), svc.Status)
		}
		pad := 9 - len(svc.Status)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(out, "%-12s %s%*s %-7d %-24s %s\n", name, status, pad, "", svc.PID, strings.Join(ips, " "), strings.Join(ports, " "))
	}
}
