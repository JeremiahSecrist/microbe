package flakegen

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
)

// RenderGenerated emits the generated.nix bridge file (spec §9.2): per-service
// cid/macs/ips/gateway/prefix/hosts plus the systemd-networkd units (spec
// §8.3). The first declared network gets a bare gateway default route; later
// networks get explicit subnet routes only.
func (st *Stack) RenderGenerated() (string, error) {
	hosts := st.Hosts()
	hostsVal := make([]any, 0, len(hosts))
	for _, h := range hosts {
		hostsVal = append(hostsVal, map[string]any{
			"ip":    h.IP,
			"names": h.Names,
		})
	}

	services := map[string]any{}
	for _, name := range sortedServiceNames(st) {
		s := st.Services[name]
		networkd, err := renderNetworkd(name, s)
		if err != nil {
			return "", err
		}
		taps := map[string]string{}
		for _, net := range s.Networks {
			taps[net] = s.Taps[net]
		}
		svc := map[string]any{
			"cid":      s.CID,
			"macs":     s.MACs,
			"ips":      s.IPs,
			"gateway":  s.Gateway,
			"prefix":   s.Prefix,
			"hosts":    hostsVal,
			"networkd": networkd,
			"taps":     taps,
		}
		if len(s.VolumeImages) > 0 {
			volumes := map[string]any{}
			for volName, path := range s.VolumeImages {
				volumes[volName] = map[string]any{"image": path}
			}
			svc["volumes"] = volumes
		}
		services[name] = svc
	}
	root := map[string]any{"services": services}
	if st.SSHPublicKey != "" {
		root["sshPublicKey"] = st.SSHPublicKey
	}
	return nixify(root) + "\n", nil
}

func renderNetworkd(svcName string, s Service) (map[string]any, error) {
	out := map[string]any{}
	for i, netName := range s.Networks {
		addr := s.IPs[netName] + "/" + strconv.Itoa(s.Prefix[netName])
		routes, err := routesFor(i, netName, s)
		if err != nil {
			return nil, err
		}
		out["mvc-"+svcName+"-"+netName] = map[string]any{
			"matchConfig": map[string]string{"MACAddress": s.MACs[netName]},
			"linkConfig":  map[string]string{"RequiredForOnline": "no"},
			"address":     []string{addr},
			"routes":      routes,
		}
	}
	return out, nil
}

func routesFor(idx int, netName string, s Service) ([]any, error) {
	gateway := s.Gateway[netName]
	if idx == 0 {
		return []any{map[string]string{"Gateway": gateway}}, nil
	}
	subnet, err := subnetOf(s.IPs[netName], s.Prefix[netName])
	if err != nil {
		return nil, fmt.Errorf("network %q: %w", netName, err)
	}
	route := map[string]string{"Destination": subnet, "Gateway": gateway}
	return []any{route}, nil
}

func subnetOf(ip string, bits int) (string, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "", err
	}
	return netip.PrefixFrom(a, bits).Masked().String(), nil
}

func sortedServiceNames(st *Stack) []string {
	names := make([]string, 0, len(st.Services))
	for name := range st.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
