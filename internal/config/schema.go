package config

import "encoding/json"

// CurrentSchemaVersion is the only Compose.SchemaVersion value this package
// accepts. Load and Parse default a missing/zero version to it; Validate
// rejects any other value.
const CurrentSchemaVersion = 1

// Compose is a parsed and defaulted (but not yet validated) stack
// definition, as produced by Parse or Load.
type Compose struct {
	SchemaVersion int                `json:"schemaVersion"`
	Name          string             `json:"name"`
	Networks      map[string]Network `json:"networks"`
	Services      map[string]Service `json:"services"`
	Rules         []Rule             `json:"rules,omitempty"`
}

// Network is a logical label a stack's services can attach to. It carries no
// address space of its own -- every service shares the same flat host-wide
// IPv6 /64 (see internal/hostnet.Plan / internal/lockfile) -- and exists so
// Service.Networks and Rule.From/To have a grouping/documentation vocabulary,
// and so Internal has somewhere to be declared.
type Network struct {
	// Internal, when true, airgaps this network from the host's own
	// internet access: no NAT64 egress, so services whose *every* network
	// attachment is internal can't dial out. Published ports (Service.Ports)
	// still work -- that's an explicit, per-service opt-in to inbound
	// reachability, orthogonal to outbound egress. Mirrors docker-compose's
	// `internal: true`.
	Internal bool `json:"internal,omitempty"`
}

// Rule grants one-way reachability from the From service to the To service.
// Services are otherwise mutually unreachable (default-deny): a service can
// only reach another if an explicit Rule names that pair. Return traffic for
// an allowed connection is handled by connection tracking, not a mirrored
// rule.
type Rule struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Ports []int  `json:"ports,omitempty"` // empty = all ports for Proto
	Proto string `json:"proto,omitempty"` // "tcp" (default) or "udp"
}

// Service is one VM's worth of configuration within a stack: sizing,
// hypervisor choice, storage, network attachments, exposed ports, and
// startup ordering.
type Service struct {
	VCPUs         int          `json:"vcpu"`
	Mem           int          `json:"mem"`
	Hypervisor    string       `json:"hypervisor"`
	OS            string       `json:"os,omitempty"`
	ConfigPresent bool         `json:"configPresent,omitempty"`
	Volumes       []Volume     `json:"volumes,omitempty"`
	Networks      []Attach     `json:"networks,omitempty"`
	Ports         []string     `json:"ports,omitempty"`
	DependsOn     []string     `json:"dependsOn,omitempty"`
	Healthcheck   *Healthcheck `json:"healthcheck,omitempty"`
}

// Volume describes storage attached to a Service: either a "disk" (a named,
// sized virtual disk) or a "share" (a host directory shared into the VM).
type Volume struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Target   string `json:"target"`
	Size     string `json:"size,omitempty"`
	Host     string `json:"host,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	FsType   string `json:"fsType,omitempty"`

	// Owner, when set on a share volume, is the guest username that should
	// own the mounted files -- virtiofsd translates between this guest
	// uid/gid (resolved from the guest's own NixOS user database, see
	// renderer.nix) and whatever uid/gid actually owns Host on the CLI's
	// host (see internal/cmd/up.go's attachShareOwners).
	Owner string `json:"owner,omitempty"`
}

// Attach binds a Service to a Network, optionally pinning the service to a
// static IPv6 address (must fall inside the host's persisted ULA /64 -- see
// internal/lockfile). Every attachment of the same service must agree on
// Addr if more than one sets it; the service's one address is shared across
// all of its network attachments (see internal/hostnet.Plan). In JSON it
// accepts either a bare network name string or the full {"name", "addr"}
// object (see UnmarshalJSON).
type Attach struct {
	Name string `json:"name"`
	Addr string `json:"addr,omitempty"`
}

// UnmarshalJSON accepts either a bare network name string (shorthand for
// {"name": "..."}), or the full {"name", "addr"} object form.
func (a *Attach) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		a.Name = name
		return nil
	}
	type attachAlias Attach
	var full attachAlias
	if err := json.Unmarshal(b, &full); err != nil {
		return err
	}
	*a = Attach(full)
	return nil
}

// Healthcheck configures a Service's TCP-socket liveness probe: the CLI
// dials Port on the service's primary network IP every Interval until it
// accepts a connection or StartPeriod elapses.
type Healthcheck struct {
	Interval    string `json:"interval"`
	Timeout     string `json:"timeout"`
	StartPeriod string `json:"startPeriod"`
	Port        int    `json:"port"`
}
