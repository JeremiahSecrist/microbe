package config

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func (c *Compose) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("config: unsupported schemaVersion %d", c.SchemaVersion)
	}
	if !nameRe.MatchString(c.Name) {
		return fmt.Errorf("config: invalid stack name %q", c.Name)
	}
	if len(c.Networks) == 0 {
		return fmt.Errorf("config: at least one network required")
	}
	for name := range c.Networks {
		if !nameRe.MatchString(name) {
			return fmt.Errorf("config: invalid network name %q", name)
		}
	}
	for name := range c.Services {
		if !nameRe.MatchString(name) {
			return fmt.Errorf("config: invalid service name %q", name)
		}
	}

	if err := c.validateSubnets(); err != nil {
		return err
	}
	if err := c.validateAttaches(); err != nil {
		return err
	}
	if err := c.validatePorts(); err != nil {
		return err
	}
	if err := c.validateDependsOn(); err != nil {
		return err
	}
	if err := c.validateVolumes(); err != nil {
		return err
	}
	return nil
}

func (c *Compose) validateSubnets() error {
	for name, net := range c.Networks {
		p, err := netip.ParsePrefix(net.Subnet)
		if err != nil {
			return fmt.Errorf("config: network %q: invalid subnet %q", name, net.Subnet)
		}
		if !p.Addr().Is4() || p.Bits() < 16 || p.Bits() > 30 {
			return fmt.Errorf("config: network %q: subnet must be an IPv4 /16../30", name)
		}
	}
	names := make([]string, 0, len(c.Networks))
	for name := range c.Networks {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a := c.Networks[names[i]]
			b := c.Networks[names[j]]
			if prefixesOverlap(a.Subnet, b.Subnet) {
				return fmt.Errorf("config: networks %q and %q overlap", names[i], names[j])
			}
		}
	}
	return nil
}

func (c *Compose) validateAttaches() error {
	staticByNet := map[string]map[string]string{}
	for netName := range c.Networks {
		staticByNet[netName] = map[string]string{}
	}
	for svcName, svc := range c.Services {
		for _, a := range svc.Networks {
			net, ok := c.Networks[a.Name]
			if !ok {
				return fmt.Errorf("config: service %q: unknown network %q", svcName, a.Name)
			}
			if a.IP == "" {
				continue
			}
			ip, err := netip.ParseAddr(a.IP)
			if err != nil {
				return fmt.Errorf("config: service %q: invalid ip %q", svcName, a.IP)
			}
			p, err := netip.ParsePrefix(net.Subnet)
			if err != nil {
				return err
			}
			if !p.Contains(ip) {
				return fmt.Errorf("config: service %q: ip %q outside %q", svcName, a.IP, net.Subnet)
			}
			if ip == p.Masked().Addr() || ip == gateway(p) || ip == broadcast(p) {
				return fmt.Errorf("config: service %q: ip %q is reserved in %q", svcName, a.IP, net.Subnet)
			}
			if prev, dup := staticByNet[a.Name][a.IP]; dup {
				return fmt.Errorf("config: duplicate static ip %q on %q (%s, %s)", a.IP, a.Name, prev, svcName)
			}
			staticByNet[a.Name][a.IP] = svcName
		}
	}
	return nil
}

func (c *Compose) validatePorts() error {
	seen := map[string]string{}
	for svcName, svc := range c.Services {
		for _, pm := range svc.Ports {
			host := hostPort(pm)
			if host == "" {
				return fmt.Errorf("config: service %q: invalid port mapping %q", svcName, pm)
			}
			if prev, dup := seen[host]; dup {
				return fmt.Errorf("config: duplicate host port %s (%s, %s)", host, prev, svcName)
			}
			seen[host] = svcName
		}
	}
	return nil
}

func (c *Compose) validateDependsOn() error {
	for svcName, svc := range c.Services {
		for _, dep := range svc.DependsOn {
			if dep == svcName {
				return fmt.Errorf("config: service %q depends on itself", svcName)
			}
			if _, ok := c.Services[dep]; !ok {
				return fmt.Errorf("config: service %q: unknown dependsOn %q", svcName, dep)
			}
		}
	}
	return c.detectCycle()
}

func (c *Compose) detectCycle() error {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		color[name] = grey
		for _, dep := range c.Services[name].DependsOn {
			switch color[dep] {
			case grey:
				return fmt.Errorf("config: dependsOn cycle: %s", strings.Join(append(path, name), " -> "))
			case white:
				if err := visit(dep, append(path, name)); err != nil {
					return err
				}
			}
		}
		color[name] = black
		return nil
	}
	for name := range c.Services {
		if color[name] == white {
			if err := visit(name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Compose) validateVolumes() error {
	diskNames := map[string]string{}
	for svcName, svc := range c.Services {
		targets := map[string]bool{}
		for _, v := range svc.Volumes {
			switch v.Type {
			case "disk":
				if v.Name == "" {
					return fmt.Errorf("config: service %q: disk volume missing name", svcName)
				}
				if prev, dup := diskNames[v.Name]; dup {
					return fmt.Errorf("config: duplicate disk volume %q (%s, %s)", v.Name, prev, svcName)
				}
				diskNames[v.Name] = svcName
			case "share":
				if v.Host == "" {
					return fmt.Errorf("config: service %q: share volume missing host", svcName)
				}
			default:
				return fmt.Errorf("config: service %q: unknown volume type %q", svcName, v.Type)
			}
			if !strings.HasPrefix(v.Target, "/") {
				return fmt.Errorf("config: service %q: volume target %q not absolute", svcName, v.Target)
			}
			if targets[v.Target] {
				return fmt.Errorf("config: service %q: duplicate volume target %q", svcName, v.Target)
			}
			targets[v.Target] = true
		}
	}
	return nil
}

func prefixesOverlap(a, b string) bool {
	pa, err := netip.ParsePrefix(a)
	if err != nil {
		return false
	}
	pb, err := netip.ParsePrefix(b)
	if err != nil {
		return false
	}
	return pa.Overlaps(pb)
}

func gateway(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	a[3]++
	return netip.AddrFrom4(a)
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

func hostPort(pm string) string {
	host := pm
	if j := strings.Index(host, "/"); j >= 0 {
		host = host[:j]
	}
	parts := strings.Split(host, ":")
	switch len(parts) {
	case 2:
		host = parts[0]
	case 3:
		host = parts[1]
	default:
		return ""
	}
	if n, err := strconv.Atoi(host); err == nil && n >= 1 && n <= 65535 {
		return strconv.Itoa(n)
	}
	return ""
}
