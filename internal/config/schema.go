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
}

// Network is a single virtual network a stack's services can attach to.
type Network struct {
	Subnet string `json:"subnet"`

	// Internal, when true, airgaps this network from the host's own
	// internet access: no masquerade, so services on it can't dial out.
	// Published ports (Service.Ports) still work — that's an explicit,
	// per-service opt-in to inbound reachability, orthogonal to outbound
	// egress. Mirrors docker-compose's `internal: true`.
	Internal bool `json:"internal,omitempty"`
}

// Service is one VM's worth of configuration within a stack: sizing,
// hypervisor choice, storage, network attachments, exposed ports, and
// startup ordering.
type Service struct {
	VCPUs         int          `json:"vcpu"`
	Mem           int          `json:"mem"`
	Hypervisor    string       `json:"hypervisor"`
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
}

// Attach binds a Service to a Network, optionally pinning it to a static
// IP. In JSON it accepts either a bare network name string or the full
// {"name", "ip"} object (see UnmarshalJSON).
type Attach struct {
	Name string `json:"name"`
	IP   string `json:"ip,omitempty"`
}

// UnmarshalJSON accepts either a bare network name string (shorthand for
// {"name": "..."}), or the full {"name", "ip"} object form.
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
