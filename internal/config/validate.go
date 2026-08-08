package config

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"microbe/internal/netutil"
)

// maxNameLen is the longest permitted stack, network, or service name
// (excluding the required first character), matched by nameRe.
const maxNameLen = 31

// minSubnetBits and maxSubnetBits bound the accepted network prefix length:
// large enough to always have a usable host range, small enough to keep
// per-network address space reasonable.
const (
	minSubnetBits = 16
	maxSubnetBits = 30
)

// minHostPort and maxHostPort are the valid range for a TCP/UDP port number.
const (
	minHostPort = 1
	maxHostPort = 65535
)

var nameRe = regexp.MustCompile(fmt.Sprintf(`^[a-z][a-z0-9_-]{0,%d}$`, maxNameLen))

// Validate checks that c is internally consistent: schema version,
// name formats, subnet layout, service network attachments, port
// uniqueness, dependsOn references, and volume definitions. It returns the
// first error found.
func (c *Compose) Validate() error {
	if c.SchemaVersion != CurrentSchemaVersion {
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
	if err := c.validateHealthchecks(); err != nil {
		return err
	}
	return nil
}

func (c *Compose) validateSubnets() error {
	prefixes := make(map[string]netip.Prefix, len(c.Networks))
	for name, net := range c.Networks {
		prefix, err := netip.ParsePrefix(net.Subnet)
		if err != nil {
			return fmt.Errorf("config: network %q: invalid subnet %q: %w", name, net.Subnet, err)
		}
		if !prefix.Addr().Is4() || prefix.Bits() < minSubnetBits || prefix.Bits() > maxSubnetBits {
			return fmt.Errorf("config: network %q: subnet must be an IPv4 /%d../%d", name, minSubnetBits, maxSubnetBits)
		}
		prefixes[name] = prefix
	}
	names := make([]string, 0, len(c.Networks))
	for name := range c.Networks {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if prefixes[names[i]].Overlaps(prefixes[names[j]]) {
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
		for _, attach := range svc.Networks {
			net, ok := c.Networks[attach.Name]
			if !ok {
				return fmt.Errorf("config: service %q: unknown network %q", svcName, attach.Name)
			}
			if attach.IP == "" {
				continue
			}
			ip, err := netip.ParseAddr(attach.IP)
			if err != nil {
				return fmt.Errorf("config: service %q: invalid ip %q: %w", svcName, attach.IP, err)
			}
			prefix, err := netip.ParsePrefix(net.Subnet)
			if err != nil {
				return fmt.Errorf("config: network %q: invalid subnet %q: %w", attach.Name, net.Subnet, err)
			}
			if !prefix.Contains(ip) {
				return fmt.Errorf("config: service %q: ip %q outside %q", svcName, attach.IP, net.Subnet)
			}
			if ip == prefix.Masked().Addr() || ip == netutil.Gateway(prefix) || ip == netutil.Broadcast(prefix) {
				return fmt.Errorf("config: service %q: ip %q is reserved in %q", svcName, attach.IP, net.Subnet)
			}
			if prev, dup := staticByNet[attach.Name][attach.IP]; dup {
				return fmt.Errorf("config: duplicate static ip %q on %q (%s, %s)", attach.IP, attach.Name, prev, svcName)
			}
			staticByNet[attach.Name][attach.IP] = svcName
		}
	}
	return nil
}

func (c *Compose) validatePorts() error {
	seen := map[string]string{}
	for svcName, svc := range c.Services {
		for _, portMapping := range svc.Ports {
			host := hostPort(portMapping)
			if host == "" {
				return fmt.Errorf("config: service %q: invalid port mapping %q", svcName, portMapping)
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
		shareNames := map[string]bool{}
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
				if v.Name == "" {
					return fmt.Errorf("config: service %q: share volume missing name", svcName)
				}
				if shareNames[v.Name] {
					return fmt.Errorf("config: service %q: duplicate share volume name %q", svcName, v.Name)
				}
				shareNames[v.Name] = true
				if v.Host == "" {
					return fmt.Errorf("config: service %q: share volume missing host", svcName)
				}
				if v.Protocol != "9p" && v.Protocol != "virtiofs" {
					return fmt.Errorf("config: service %q: share volume has unknown protocol %q (want 9p or virtiofs)", svcName, v.Protocol)
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

func (c *Compose) validateHealthchecks() error {
	for svcName, svc := range c.Services {
		hc := svc.Healthcheck
		if hc == nil {
			continue
		}
		if hc.Port < minHostPort || hc.Port > maxHostPort {
			return fmt.Errorf("config: service %q: healthcheck port %d out of range", svcName, hc.Port)
		}
		for field, value := range map[string]string{
			"interval":    hc.Interval,
			"timeout":     hc.Timeout,
			"startPeriod": hc.StartPeriod,
		} {
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("config: service %q: invalid healthcheck %s %q: %w", svcName, field, value, err)
			}
		}
	}
	return nil
}

// hostPort extracts the host-side port number from a Docker Compose-style
// port mapping (e.g. "8080:80", "127.0.0.1:8080:80/tcp"), returning it
// normalized without leading zeros, or "" if the mapping is malformed.
func hostPort(portMapping string) string {
	host := portMapping
	if slash := strings.Index(host, "/"); slash >= 0 {
		host = host[:slash]
	}
	fields := strings.Split(host, ":")
	switch len(fields) {
	case 2: // hostPort:containerPort
		host = fields[0]
	case 3: // hostIP:hostPort:containerPort
		host = fields[1]
	default:
		return ""
	}
	if port, err := strconv.Atoi(host); err == nil && port >= minHostPort && port <= maxHostPort {
		return strconv.Itoa(port)
	}
	return ""
}
