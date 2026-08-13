package flakegen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// RenderGenerated emits the generated.json bridge file (spec §9.2): per-service
// cid/macs/addr/gateway/prefix/hosts plus the systemd-networkd units (spec
// §8.3). Every attachment shares the service's one address; only the first
// declared network gets a default route, since the whole /64 is reachable
// via the shared host gateway regardless of which tap traffic entered on.
// Plain JSON, not Nix: the .json extension signals at a glance that it's
// CLI-emitted data, not something to hand-edit, and it's read back with
// builtins.fromJSON (see modules/renderer.nix).
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
		taps := map[string]string{}
		for _, net := range s.Networks {
			taps[net] = s.Taps[net]
		}
		svc := map[string]any{
			"cid":         s.CID,
			"macs":        s.MACs,
			"addr":        s.Addr,
			"gateway":     s.Gateway,
			"prefix":      s.Prefix,
			"hosts":       hostsVal,
			"networkd":    renderNetworkd(name, s),
			"taps":        taps,
			"buildTarget": s.BuildTarget,
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
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render generated: %w", err)
	}
	return string(out) + "\n", nil
}

func renderNetworkd(svcName string, s Service) map[string]any {
	addr := s.Addr + "/" + strconv.Itoa(s.Prefix)
	out := map[string]any{}
	for i, netName := range s.Networks {
		// systemd-networkd's routes option rejects null (only [] or a list
		// of route attrsets), so a secondary attachment -- which gets no
		// route of its own, since the whole /64 is reachable via whichever
		// tap the primary's default route already uses -- must marshal to
		// an empty JSON array, not Go's nil-slice-as-null.
		routes := []any{}
		if i == 0 {
			routes = []any{map[string]string{"Gateway": s.Gateway}}
		}
		out["mvc-"+svcName+"-"+netName] = map[string]any{
			"matchConfig": map[string]string{"MACAddress": s.MACs[netName]},
			"linkConfig":  map[string]string{"RequiredForOnline": "no"},
			"address":     []string{addr},
			"routes":      routes,
		}
	}
	return out
}

func sortedServiceNames(st *Stack) []string {
	names := make([]string, 0, len(st.Services))
	for name := range st.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
