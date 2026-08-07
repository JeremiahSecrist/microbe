package hostnet

import (
	"fmt"
	"net/netip"
	"sort"

	"micro-compose/internal/config"
	"micro-compose/internal/netutil"
)

type NetworkPlan struct {
	IPs  map[string]map[string]string
	MACs map[string]map[string]string
}

func Plan(cfg *config.Compose) (*NetworkPlan, error) {
	ips, err := allocateIPs(cfg)
	if err != nil {
		return nil, err
	}
	return &NetworkPlan{IPs: ips, MACs: allocateMACs(cfg)}, nil
}

func allocateIPs(cfg *config.Compose) (map[string]map[string]string, error) {
	ips := map[string]map[string]string{}
	for svc := range cfg.Services {
		ips[svc] = map[string]string{}
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
				ips[svcName][netName] = ip
				continue
			}
			ip, err := nextFree(net.Subnet, used)
			if err != nil {
				return nil, fmt.Errorf("network %q: %w", netName, err)
			}
			used[ip] = true
			ips[svcName][netName] = ip
		}
	}
	return ips, nil
}

func allocateMACs(cfg *config.Compose) map[string]map[string]string {
	macs := map[string]map[string]string{}
	for svc := range cfg.Services {
		macs[svc] = map[string]string{}
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
		macs[pr.svc][pr.net] = fmt.Sprintf("02:00:00:00:00:%02x", i+1)
	}
	return macs
}

func RenderHosts(p *NetworkPlan) []string {
	var svcs []string
	for svc := range p.IPs {
		svcs = append(svcs, svc)
	}
	sort.Strings(svcs)
	var out []string
	for _, svc := range svcs {
		var nets []string
		for net := range p.IPs[svc] {
			nets = append(nets, net)
		}
		sort.Strings(nets)
		for _, net := range nets {
			out = append(out, fmt.Sprintf("%s %s %s.%s", p.IPs[svc][net], svc, svc, net))
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
	start := netutil.Gateway(p).Next()
	end := netutil.Broadcast(p).Prev()
	for a := start; a.IsValid() && a.Compare(end) <= 0; a = a.Next() {
		s := a.String()
		if !used[s] {
			return s, nil
		}
	}
	return "", fmt.Errorf("no free host in %s", subnet)
}
