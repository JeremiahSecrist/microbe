package config

import "encoding/json"

type Compose struct {
	SchemaVersion int                `json:"schemaVersion"`
	Name          string             `json:"name"`
	Networks      map[string]Network `json:"networks"`
	Services      map[string]Service `json:"services"`
}

type Network struct {
	Subnet string `json:"subnet"`
}

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

type Attach struct {
	Name string `json:"name"`
	IP   string `json:"ip,omitempty"`
}

func (a *Attach) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		a.Name = name
		return nil
	}
	type alias Attach
	var x alias
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	*a = Attach(x)
	return nil
}

type Healthcheck struct {
	Interval    string   `json:"interval"`
	Timeout     string   `json:"timeout"`
	StartPeriod string   `json:"startPeriod"`
	Command     []string `json:"command"`
}
