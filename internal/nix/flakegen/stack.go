package flakegen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"

	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/netutil"
)

// Stack is the CLI-side model of a rendered stack: enough data to emit
// generated.nix (spec §9.2) and the flake (spec §9.3).
type Stack struct {
	Name     string
	Services map[string]Service
}

type Service struct {
	CID      int
	Networks []string // declared order (first is primary default route)
	MACs     map[string]string
	IPs      map[string]string
	Gateway  map[string]string
	Prefix   map[string]int
	Taps     map[string]string // net -> host tap id (≤15 chars)

	// VolumeImages maps disk volume name to its absolute qcow2 path on the
	// host, populated by the caller (up.go knows the CLI's base dir). Empty
	// unless the service declares disk volumes.
	VolumeImages map[string]string
}

// Host is one /etc/hosts entry shared by every guest.
type Host struct {
	IP    string
	Names []string
}

// FromConfig builds a Stack from a validated compose file and its network
// plan. CIDs are assigned 3, 4, ... in service-name order (vsock convention
// reserves 0-2 for host/services).
func FromConfig(cfg *config.Compose, plan *hostnet.NetworkPlan) (*Stack, error) {
	st := &Stack{Name: cfg.Name, Services: map[string]Service{}}
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		svcCfg := cfg.Services[name]
		s := Service{
			CID:      i + 3,
			Networks: declaredNets(svcCfg),
			MACs:     plan.MACs[name],
			IPs:      plan.IPs[name],
			Gateway:  map[string]string{},
			Prefix:   map[string]int{},
			Taps:     map[string]string{},
		}
		for _, netName := range s.Networks {
			p, err := netip.ParsePrefix(cfg.Networks[netName].Subnet)
			if err != nil {
				return nil, fmt.Errorf("service %q network %q: %w", name, netName, err)
			}
			s.Gateway[netName] = netutil.Gateway(p).String()
			s.Prefix[netName] = p.Bits()
			s.Taps[netName] = TapID(cfg.Name, name, netName)
		}
		st.Services[name] = s
	}
	return st, nil
}

func declaredNets(svc config.Service) []string {
	out := make([]string, 0, len(svc.Networks))
	for _, a := range svc.Networks {
		out = append(out, a.Name)
	}
	return out
}

// TapID returns the host-side tap name for a service's interface: readable
// "mvc-<stack>-<svc>-<net>" when it fits, else a deterministic 15-char
// hash-based name (Linux interface names are capped at IFNAMSIZ=15).
func TapID(stack, svc, net string) string {
	full := "mvc-" + stack + "-" + svc + "-" + net
	if len(full) <= 15 {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return "mvc-" + hex.EncodeToString(sum[:])[:11]
}

// Names returns the sorted service names in the stack.
func (st *Stack) Names() []string {
	return sortedServiceNames(st)
}

// Hosts returns the /etc/hosts entries shared by every guest, ordered by
// service then network.
func (st *Stack) Hosts() []Host {
	svcs := make([]string, 0, len(st.Services))
	for name := range st.Services {
		svcs = append(svcs, name)
	}
	sort.Strings(svcs)
	var out []Host
	for _, name := range svcs {
		s := st.Services[name]
		nets := append([]string(nil), s.Networks...)
		sort.Strings(nets)
		for _, net := range nets {
			out = append(out, Host{
				IP:    s.IPs[net],
				Names: []string{name, name + "." + net},
			})
		}
	}
	return out
}
