package flakegen

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// RenderGenerated emits the generated.json bridge file (spec §9.2): per-service
// cid/macs/ips/gateway/prefix/hosts plus the systemd-networkd units (spec
// §8.3). The first declared network gets a bare gateway default route; later
// networks get explicit subnet routes only. Plain JSON, not Nix: the .json
// extension signals at a glance that it's CLI-emitted data, not something to
// hand-edit, and it's read back with builtins.fromJSON (see modules/renderer.nix).
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
			"cid":         s.CID,
			"macs":        s.MACs,
			"ips":         s.IPs,
			"gateway":     s.Gateway,
			"prefix":      s.Prefix,
			"hosts":       hostsVal,
			"networkd":    networkd,
			"taps":        taps,
			"buildTarget": s.BuildTarget,
		}
		if len(s.VolumeImages) > 0 || len(s.ShareOwners) > 0 || len(s.ShareHosts) > 0 {
			volumes := map[string]any{}
			entry := func(volName string) map[string]any {
				e, _ := volumes[volName].(map[string]any)
				if e == nil {
					e = map[string]any{}
					volumes[volName] = e
				}
				return e
			}
			for volName, path := range s.VolumeImages {
				entry(volName)["image"] = path
			}
			for volName, host := range s.ShareHosts {
				entry(volName)["host"] = host
			}
			for volName, owner := range s.ShareOwners {
				e := entry(volName)
				e["hostUid"] = owner.HostUID
				e["hostGid"] = owner.HostGID
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

// LoadGeneratedCID reads dir/generated.json (written by WriteStack) and
// returns svc's assigned vsock CID. Used at `microbe shell`/`exec` time for
// finix guests, which reach their agent over real AF_VSOCK (addressed by
// CID) rather than the nixos path's hybrid-vsock UDS -- unlike that UDS
// (a filesystem path the guest's compose config already carries around),
// the CID has to be looked up, since it's assigned once at render time and
// otherwise only known to the rendered Nix (finix-agent.nix reads the same
// file to wire the QEMU vsock device with the same value).
func LoadGeneratedCID(dir, svc string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dir, "generated.json"))
	if err != nil {
		return 0, fmt.Errorf("load generated.json: %w", err)
	}
	var root struct {
		Services map[string]struct {
			CID int `json:"cid"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return 0, fmt.Errorf("load generated.json: %w", err)
	}
	entry, ok := root.Services[svc]
	if !ok {
		return 0, fmt.Errorf("load generated.json: no service %q", svc)
	}
	return entry.CID, nil
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
