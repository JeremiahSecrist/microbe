package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	DefaultVCPUs      = 1
	DefaultMem        = 512
	DefaultHypervisor = "cloud-hypervisor"
	DefaultOS         = "nixos"
)

// Parse decodes a Compose document from JSON and fills in any omitted
// fields (schema version, per-service resource sizing, volume defaults)
// with their standard values. It does not validate the result; call
// Compose.Validate for that.
func Parse(data []byte) (*Compose, error) {
	var c Compose
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	applyDefaults(&c)
	return &c, nil
}

// applyDefaults fills in zero-valued fields of c with the package's
// standard defaults, in place.
func applyDefaults(c *Compose) {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = CurrentSchemaVersion
	}
	for name, svc := range c.Services {
		if svc.VCPUs == 0 {
			svc.VCPUs = DefaultVCPUs
		}
		if svc.Mem == 0 {
			svc.Mem = DefaultMem
		}
		if svc.Hypervisor == "" {
			svc.Hypervisor = DefaultHypervisor
		}
		if svc.OS == "" {
			svc.OS = DefaultOS
		}
		for i := range svc.Volumes {
			v := &svc.Volumes[i]
			if v.Type == "" {
				v.Type = "share"
			}
			if v.Type == "share" {
				if v.Mode == "" {
					v.Mode = "rw"
				}
				if v.Protocol == "" {
					// cloud-hypervisor (the default hypervisor) only
					// supports virtiofs shares, not 9p.
					v.Protocol = "virtiofs"
				}
			} else if v.Type == "disk" {
				if v.FsType == "" {
					v.FsType = "ext4"
				}
			}
		}
		if svc.Healthcheck != nil {
			if svc.Healthcheck.Interval == "" {
				svc.Healthcheck.Interval = "5s"
			}
			if svc.Healthcheck.Timeout == "" {
				svc.Healthcheck.Timeout = "2s"
			}
			if svc.Healthcheck.StartPeriod == "" {
				svc.Healthcheck.StartPeriod = "10s"
			}
		}
		c.Services[name] = svc
	}
}

// Load reads a Compose stack from path, evaluating it with Eval if it's a
// Nix module (".nix" suffix) or parsing it directly as JSON otherwise.
func Load(path string) (*Compose, error) {
	var data []byte
	var err error
	if strings.HasSuffix(path, ".nix") {
		data, err = Eval(path)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}
