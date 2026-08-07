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
	VCPUs       int          `json:"vcpu"`
	Mem         int          `json:"mem"`
	Hypervisor  string       `json:"hypervisor"`
	Config      interface{}  `json:"config"`
	Volumes     []Volume     `json:"volumes"`
	Networks    []Attach     `json:"networks"`
	Ports       []string     `json:"ports"`
	DependsOn   []string     `json:"dependsOn"`
	Healthcheck *Healthcheck `json:"healthcheck"`
}

type Volume struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Target   string `json:"target"`
	Size     string `json:"size"`
	Host     string `json:"host"`
	Mode     string `json:"mode"`
	Protocol string `json:"protocol"`
	FsType   string `json:"fsType"`
}

type Attach struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
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
