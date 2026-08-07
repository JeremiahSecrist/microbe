package hostnet

import (
	"fmt"
	"net/netip"
	"sort"

	"micro-compose/internal/config"
)

type NetworkPlan struct {
	IPs  map[string]map[string]string
	MACs map[string]map[string]string
}

func Plan(cfg *config.Compose) (*NetworkPlan, error) {
	p := &NetworkPlan{
		IPs:  map[string]map[string]string{},
		MACs: map[string]map[string]string{},
	}
	for svc := range cfg.Services {
		p.IPs[svc] = map[string]string{}
		p.MACs[svc] = map[string]string{}
	}

	for netName, net := range cfg.Networks {
		members := servicesOn(cfg, netName)
		used := map[string]bool{}
		for _, svcName := range members {
			if ip := staticIP(cfg.Services[svcName], netName); ip != "" {
				used[ip] = true
			}
		}
		for _, svcName := range members {
			if ip := staticIP(cfg.Services[svcName], netName); ip != "" {
				p.IPs[svcName][netName] = ip
				continue
			}
			ip, err := nextFree(net.Subnet, used)
			if err != nil {
				return nil, fmt.Errorf("network %q: %w", netName, err)
			}
			used[ip] = true
			p.IPs[svcName][netName] = ip
		}
	}

	type pair struct{ svc, net string }
	var pairs []pair
	for svcName, svc := range cfg.Services {
		for _, a := range svc.Networks {
			pairs = append(pairs, pair{svcName, a.Name})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].svc != pairs[j].svc {
			return pairs[i].svc < pairs[j].svc
		}
		return pairs[i].net < pairs[j].net
	})
	for i, pr := range pairs {
		p.MACs[pr.svc][pr.net] = fmt.Sprintf("02:00:00:00:00:%02x", i+1)
	}
	return p, nil
}

func RenderHosts(p *NetworkPlan) []string {
	var svcs []string
	for svc := range p.IPs {
		svcs = append(svcs, svc)
	}
	sort.Strings(svcs)
	var out []string
	for _, svc := range svcs {
		for net, ip := range p.IPs[svc] {
			out = append(out, fmt.Sprintf("%s %s %s.%s", ip, svc, svc, net))
		}
	}
	return out
}

func servicesOn(cfg *config.Compose, netName string) []string {
	var out []string
	for svcName, svc := range cfg.Services {
		for _, a := range svc.Networks {
			if a.Name == netName {
				out = append(out, svcName)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func staticIP(svc config.Service, netName string) string {
	for _, a := range svc.Networks {
		if a.Name == netName {
			return a.IP
		}
	}
	return ""
}

func nextFree(subnet string, used map[string]bool) (string, error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		return "", err
	}
	base := p.Masked().Addr().As4()
	bcast := broadcast(p).As4()
	for i := 1; i < 255; i++ {
		if i == 1 {
			continue
		}
		a := base
		a[3] += byte(i)
		if a[3] > bcast[3] {
			break
		}
		ip := netip.AddrFrom4(a).String()
		if !used[ip] {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no free host in %s", subnet)
}

func broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	bits := p.Bits()
	for i := 3; i >= 0; i-- {
		hostBits := (i+1)*8 - bits
		if hostBits <= 0 {
			break
		}
		if hostBits > 8 {
			hostBits = 8
		}
		var mask uint8
		for k := 0; k < hostBits; k++ {
			mask |= 1 << k
		}
		a[i] |= mask
	}
	return netip.AddrFrom4(a)
}
