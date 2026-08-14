package config

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxNameLen is the longest permitted stack, network, or service name
// (excluding the required first character), matched by nameRe.
const maxNameLen = 31

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
	if err := c.validateOS(); err != nil {
		return err
	}
	if err := c.validateRules(); err != nil {
		return err
	}
	return nil
}

func (c *Compose) validateOS() error {
	for svcName, svc := range c.Services {
		if svc.OS != "nixos" && svc.OS != "finix" {
			return fmt.Errorf("config: service %q: unknown os %q (want nixos or finix)", svcName, svc.OS)
		}
	}
	return nil
}

// validateAttaches checks that every attachment names a declared network,
// that a service's attachments agree on a single static Addr (a service has
// exactly one IPv6 address shared across all its network attachments -- see
// internal/hostnet.Plan), and that static addresses don't collide across
// services (one flat address space now, not one per network).
func (c *Compose) validateAttaches() error {
	staticAddrs := map[string]string{} // addr -> owning service
	for svcName, svc := range c.Services {
		var svcAddr string
		for _, attach := range svc.Networks {
			if _, ok := c.Networks[attach.Name]; !ok {
				return fmt.Errorf("config: service %q: unknown network %q", svcName, attach.Name)
			}
			if attach.Addr == "" {
				continue
			}
			addr, err := netip.ParseAddr(attach.Addr)
			if err != nil {
				return fmt.Errorf("config: service %q: invalid addr %q: %w", svcName, attach.Addr, err)
			}
			if !addr.Is6() {
				return fmt.Errorf("config: service %q: addr %q must be an IPv6 address", svcName, attach.Addr)
			}
			if svcAddr != "" && attach.Addr != svcAddr {
				return fmt.Errorf("config: service %q: conflicting static addrs %q and %q across attachments", svcName, svcAddr, attach.Addr)
			}
			svcAddr = attach.Addr
		}
		if svcAddr == "" {
			continue
		}
		if prev, dup := staticAddrs[svcAddr]; dup {
			return fmt.Errorf("config: duplicate static addr %q (%s, %s)", svcAddr, prev, svcName)
		}
		staticAddrs[svcAddr] = svcName
	}
	return nil
}

// validateRules checks that each Rule references declared services, isn't a
// self-loop, has a recognized Proto, valid Ports, and isn't a duplicate of
// another rule.
func (c *Compose) validateRules() error {
	type key struct {
		from, to, proto string
		port            int
	}
	seen := map[key]bool{}
	for i, r := range c.Rules {
		if _, ok := c.Services[r.From]; !ok {
			return fmt.Errorf("config: rule %d: unknown service %q in from", i, r.From)
		}
		if _, ok := c.Services[r.To]; !ok {
			return fmt.Errorf("config: rule %d: unknown service %q in to", i, r.To)
		}
		if r.From == r.To {
			return fmt.Errorf("config: rule %d: from and to are both %q", i, r.From)
		}
		proto := r.Proto
		if proto != "" && proto != "tcp" && proto != "udp" {
			return fmt.Errorf("config: rule %d: unknown proto %q (want tcp or udp)", i, r.Proto)
		}
		ports := r.Ports
		if len(ports) == 0 {
			ports = []int{0} // 0 = all ports, for dedup/range purposes
		}
		for _, port := range ports {
			if port != 0 && (port < minHostPort || port > maxHostPort) {
				return fmt.Errorf("config: rule %d: port %d out of range", i, port)
			}
			k := key{r.From, r.To, proto, port}
			if seen[k] {
				return fmt.Errorf("config: rule %d: duplicate rule %s -> %s (%s, port %d)", i, r.From, r.To, proto, port)
			}
			seen[k] = true
		}
	}
	return nil
}

func (c *Compose) validatePorts() error {
	seen := map[int]string{}
	for svcName, svc := range c.Services {
		for _, portMapping := range svc.Ports {
			host, _, err := ParsePort(portMapping)
			if err != nil {
				return fmt.Errorf("config: service %q: invalid port mapping %q", svcName, portMapping)
			}
			if prev, dup := seen[host]; dup {
				return fmt.Errorf("config: duplicate host port %d (%s, %s)", host, prev, svcName)
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
				if v.Owner != "" {
					return fmt.Errorf("config: service %q: disk volume %q: owner only applies to share volumes", svcName, v.Name)
				}
			case "share":
				if v.Name == "" {
					return fmt.Errorf("config: service %q: share volume missing name", svcName)
				}
				if shareNames[v.Name] {
					return fmt.Errorf("config: service %q: duplicate share volume name %q", svcName, v.Name)
				}
				shareNames[v.Name] = true
				// Host is optional: up.go's attachShareHosts defaults an
				// omitted host to a CLI-managed directory under datadir,
				// docker-style, rather than requiring an existing path.
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

// ParsePort parses a strict "hostPort:guestPort" mapping (the only form
// microbe supports -- no host-IP prefix, no /proto suffix; see
// docs/microbe-plan.md's port-mapping section for why those forms are
// deliberately rejected rather than partially honored). This is the single
// source of truth for the mapping grammar: both config.Validate and the
// runtime DNAT rule construction in internal/cmd call this, so the two can
// never drift into accepting different strings.
func ParsePort(portMapping string) (host, guest int, err error) {
	parts := strings.Split(portMapping, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port mapping %q", portMapping)
	}
	host, err = strconv.Atoi(parts[0])
	if err != nil || host < minHostPort || host > maxHostPort {
		return 0, 0, fmt.Errorf("invalid port mapping %q", portMapping)
	}
	guest, err = strconv.Atoi(parts[1])
	if err != nil || guest < minHostPort || guest > maxHostPort {
		return 0, 0, fmt.Errorf("invalid port mapping %q", portMapping)
	}
	return host, guest, nil
}
