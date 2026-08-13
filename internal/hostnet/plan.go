package hostnet

import (
	"crypto/rand"
	"fmt"
	"net/netip"
	"sort"

	"microbe/internal/config"
	"microbe/internal/lockfile"
)

// NetworkPlan is the resolved addressing for one stack: one IPv6 address per
// service (shared across all of that service's network attachments) and the
// existing per-service-per-network MAC assignment used for tap/NIC identity.
type NetworkPlan struct {
	Addrs map[string]string            // svc -> ipv6 addr (bare, no /128 suffix)
	MACs  map[string]map[string]string // svc -> net -> mac
}

// Plan resolves addresses and MACs for cfg. lock supplies (and is mutated
// with) each service's permanent address: an address already present in
// lock.Services is reused unconditionally -- addresses are generated once
// and never change -- and any service missing from lock gets a fresh random
// /128 within lock's prefix, written back into lock for the caller to
// persist (see internal/lockfile). A static config.Attach.Addr always wins
// over both the lockfile and random generation.
//
// Plan does no filesystem I/O itself; the caller loads/saves lock.
func Plan(cfg *config.Compose, lock *lockfile.Lock) (*NetworkPlan, error) {
	addrs, err := allocateAddrs(cfg, lock)
	if err != nil {
		return nil, err
	}
	return &NetworkPlan{Addrs: addrs, MACs: allocateMACs(cfg)}, nil
}

func allocateAddrs(cfg *config.Compose, lock *lockfile.Lock) (map[string]string, error) {
	if lock.Prefix == "" {
		return nil, fmt.Errorf("hostnet: lock has no prefix")
	}
	prefix, err := netip.ParsePrefix(lock.Prefix)
	if err != nil {
		return nil, fmt.Errorf("hostnet: invalid lock prefix %q: %w", lock.Prefix, err)
	}
	if lock.Services == nil {
		lock.Services = map[string]string{}
	}

	var svcNames []string
	for svc := range cfg.Services {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames)

	addrs := map[string]string{}
	for _, svc := range svcNames {
		if static := staticAddr(cfg.Services[svc]); static != "" {
			addrs[svc] = static
			lock.Services[svc] = static
			continue
		}
		if existing, ok := lock.Services[svc]; ok {
			addrs[svc] = existing
			continue
		}
		addr, err := randomAddr(prefix)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svc, err)
		}
		addrs[svc] = addr
		lock.Services[svc] = addr
	}
	return addrs, nil
}

// randomAddr draws a cryptographically random address from within prefix by
// randomizing every bit outside the prefix. Only whole-byte host portions
// are supported (i.e. prefix.Bits() a multiple of 8, as with the /64 ULA
// prefix microbe always generates) -- collision probability at 64 random
// host bits is astronomically low, so unlike the old per-network IPv4
// nextFree scan, no used-address bookkeeping or retry loop is needed.
func randomAddr(prefix netip.Prefix) (string, error) {
	if !prefix.Addr().Is6() {
		return "", fmt.Errorf("prefix %s is not IPv6", prefix)
	}
	if prefix.Bits()%8 != 0 {
		return "", fmt.Errorf("prefix %s: bit length must be a multiple of 8", prefix)
	}
	base := prefix.Masked().Addr().As16()
	hostBytes := (128 - prefix.Bits()) / 8
	buf := make([]byte, hostBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	copy(base[16-hostBytes:], buf)
	return netip.AddrFrom16(base).String(), nil
}

func allocateMACs(cfg *config.Compose) map[string]map[string]string {
	macs := map[string]map[string]string{}
	for svc := range cfg.Services {
		macs[svc] = map[string]string{}
	}
	type svcNetPair struct{ svc, net string }
	var pairs []svcNetPair
	for svcName, svc := range cfg.Services {
		for _, attach := range svc.Networks {
			pairs = append(pairs, svcNetPair{svcName, attach.Name})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].svc != pairs[j].svc {
			return pairs[i].svc < pairs[j].svc
		}
		return pairs[i].net < pairs[j].net
	})
	for i, pair := range pairs {
		macs[pair.svc][pair.net] = fmt.Sprintf("02:00:00:00:00:%02x", i+1)
	}
	return macs
}

// RenderHosts renders one /etc/hosts line per service. Every attachment of a
// service shares its one address, so unlike the old per-network model there
// is nothing distinguishing a `.net`-suffixed alias would add.
func RenderHosts(p *NetworkPlan) []string {
	var svcs []string
	for svc := range p.Addrs {
		svcs = append(svcs, svc)
	}
	sort.Strings(svcs)
	var out []string
	for _, svc := range svcs {
		out = append(out, fmt.Sprintf("%s %s", p.Addrs[svc], svc))
	}
	return out
}

func staticAddr(svc config.Service) string {
	for _, attach := range svc.Networks {
		if attach.Addr != "" {
			return attach.Addr
		}
	}
	return ""
}
