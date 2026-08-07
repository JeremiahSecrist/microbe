package hostnet

import (
	"fmt"
	"net/netip"
	"sort"

	"microbe/internal/config"
	"microbe/internal/netutil"
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
	for netName, network := range cfg.Networks {
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
			ip, err := nextFree(network.Subnet, used)
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
		for _, attach := range svc.Networks {
			if attach.Name == netName {
				out = append(out, svcName)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func staticIP(svc config.Service, netName string) string {
	for _, attach := range svc.Networks {
		if attach.Name == netName {
			return attach.IP
		}
	}
	return ""
}

func nextFree(subnet string, used map[string]bool) (string, error) {
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil {
		return "", err
	}
	start := netutil.Gateway(prefix).Next()
	end := netutil.Broadcast(prefix).Prev()
	for addr := start; addr.IsValid() && addr.Compare(end) <= 0; addr = addr.Next() {
		addrStr := addr.String()
		if !used[addrStr] {
			return addrStr, nil
		}
	}
	return "", fmt.Errorf("no free host in %s", subnet)
}
