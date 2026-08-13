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
	OS       string   // "nixos" or "finix" (config.Service.OS, copied verbatim)
	Networks []string // declared order (first is primary default route)
	MACs     map[string]string

	// Addr is this service's one IPv6 address, shared across every network
	// it attaches to (see internal/hostnet.Plan / internal/lockfile).
	Addr string
	// Gateway is the host's flat-network gateway address (one per host,
	// see internal/netutil.V6Gateway), the same for every service in every
	// stack.
	Gateway string
	// Prefix is the gateway/address prefix length -- always 64, the host's
	// persisted ULA prefix.
	Prefix int
	Taps   map[string]string // net -> host tap id (see maxTapNameLen)

	// BuildTarget is the nix attrpath ServicePart's rendered flake.nix
	// exposes for this service's bootable runner derivation, computed from
	// OS. `nix build <BuildTarget>` (relative to the rendered project dir)
	// produces the same kind of runner regardless of guest OS.
	BuildTarget string

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
// plan. prefix is the host's persisted flat IPv6 ULA (see
// internal/lockfile.Lock.Prefix / internal/state.HostState) -- every
// service's Gateway/Prefix is derived from it, the same for the whole
// stack. CIDs are assigned starting at firstGuestCID in service-name order.
func FromConfig(cfg *config.Compose, plan *hostnet.NetworkPlan, prefix string) (*Stack, error) {
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("host prefix %q: %w", prefix, err)
	}
	gateway := netutil.V6Gateway(p).String()

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
			CID:         i + firstGuestCID,
			OS:          svcCfg.OS,
			BuildTarget: buildTarget(name, svcCfg.OS),
			Networks:    declaredNets(svcCfg),
			MACs:        plan.MACs[name],
			Addr:        plan.Addrs[name],
			Gateway:     gateway,
			Prefix:      p.Bits(),
			Taps:        map[string]string{},
		}
		for _, netName := range s.Networks {
			s.Taps[netName] = TapID(cfg.Name, name, netName)
		}
		st.Services[name] = s
	}
	return st, nil
}

// buildTarget returns the nix attrpath (relative to the rendered project's
// flake.nix) for a service's bootable runner derivation. Empty os is
// treated as "nixos" (config.Compose.Validate rejects any other empty
// value once parsed through config.Parse, but callers constructing a
// config.Service literal directly, as tests do, may leave it zero-valued).
func buildTarget(name, os string) string {
	if os == "finix" {
		return ".#finixConfigurations." + name + ".config.microbe.qemuRunner"
	}
	return ".#nixosConfigurations." + name + ".config.microvm.declaredRunner"
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

// Hosts returns the /etc/hosts entries shared by every guest, one per
// service: every attachment shares the same Addr, so there's nothing a
// second, `.net`-suffixed entry would add.
func (st *Stack) Hosts() []Host {
	svcs := make([]string, 0, len(st.Services))
	for name := range st.Services {
		svcs = append(svcs, name)
	}
	sort.Strings(svcs)
	var out []Host
	for _, name := range svcs {
		out = append(out, Host{IP: st.Services[name].Addr, Names: []string{name}})
	}
	return out
}
