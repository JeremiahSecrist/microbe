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

const (
	// firstGuestCID is the vsock CID assigned to the first service in a
	// stack. CIDs 0-2 are reserved by vsock convention for the host and
	// well-known services, so guest CIDs start counting up from here.
	firstGuestCID = 3

	// maxTapNameLen is the longest tap interface name the kernel accepts
	// (Linux caps interface names, including the NUL terminator, at
	// IFNAMSIZ=16, leaving 15 usable characters).
	maxTapNameLen = 15

	// tapHashLen is the length of the hex-encoded hash suffix used for a
	// tap name once the readable form exceeds maxTapNameLen. Combined with
	// the 4-character "mvc-" prefix this yields a name exactly
	// maxTapNameLen characters long.
	tapHashLen = maxTapNameLen - len("mvc-")
)

// Stack is the CLI-side model of a rendered stack: enough data to emit
// generated.json (spec §9.2) and the flake (spec §9.3).
type Stack struct {
	Name     string
	Services map[string]Service

	// Internal marks which networks are airgapped from outbound access
	// (config.Network.Internal), keyed by network name.
	Internal map[string]bool
}

type Service struct {
	CID      int
	Networks []string // declared order (first is primary default route)
	MACs     map[string]string
	IPs      map[string]string
	Gateway  map[string]string
	Prefix   map[string]int
	Taps     map[string]string // net -> host tap id (see maxTapNameLen)

	// VolumeImages maps disk volume name to its absolute qcow2 path on the
	// host, populated by the caller (up.go knows the CLI's base dir). Empty
	// unless the service declares disk volumes.
	VolumeImages map[string]string

	// ShareOwners maps a share volume name (with Owner set) to its host
	// directory's actual owning uid/gid, populated by the caller
	// (up.go's attachShareOwners) so renderer.nix can pass it to
	// virtiofsd's --translate-uid/--translate-gid.
	ShareOwners map[string]ShareOwner

	// ShareHosts maps a share volume name that omitted "host" to the
	// CLI-managed directory path up.go's attachShareHosts defaulted it to
	// (docker-style managed volume). renderer.nix imports the user's raw
	// microbe.nix directly, so it can't see this Go-computed default
	// itself -- it falls back to generated.json (this field) when the
	// compose file's own v.host is absent.
	ShareHosts map[string]string
}

// ShareOwner is a share volume's host-directory owning uid/gid, computed
// by internal/cmd/up.go's attachShareOwners (Go-only data, like
// VolumeImages) so renderer.nix can pass it to virtiofsd's
// --translate-uid/--translate-gid.
type ShareOwner struct {
	HostUID int
	HostGID int
}

// Host is one /etc/hosts entry shared by every guest.
type Host struct {
	IP    string
	Names []string
}

// FromConfig builds a Stack from a validated compose file and its network
// plan. CIDs are assigned starting at firstGuestCID in service-name order.
func FromConfig(cfg *config.Compose, plan *hostnet.NetworkPlan) (*Stack, error) {
	st := &Stack{Name: cfg.Name, Services: map[string]Service{}, Internal: map[string]bool{}}
	for netName, net := range cfg.Networks {
		st.Internal[netName] = net.Internal
	}
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		svcCfg := cfg.Services[name]
		s := Service{
			CID:      i + firstGuestCID,
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
// "mvc-<stack>-<svc>-<net>" when it fits within maxTapNameLen, else a
// deterministic hash-based name of the same length.
func TapID(stack, svc, net string) string {
	full := "mvc-" + stack + "-" + svc + "-" + net
	if len(full) <= maxTapNameLen {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return "mvc-" + hex.EncodeToString(sum[:])[:tapHashLen]
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
