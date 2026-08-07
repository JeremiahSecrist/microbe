package config

import (
	"encoding/json"
	"fmt"
)

const (
	DefaultVCpus      = 1
	DefaultMem        = 512
	DefaultHypervisor = "cloud-hypervisor"
)

func Parse(data []byte) (*Compose, error) {
	var c Compose
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	applyDefaults(&c)
	return &c, nil
}

func applyDefaults(c *Compose) {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = 1
	}
	for name, svc := range c.Services {
		if svc.VCpus == 0 {
			svc.VCpus = DefaultVCpus
		}
		if svc.Mem == 0 {
			svc.Mem = DefaultMem
		}
		if svc.Hypervisor == "" {
			svc.Hypervisor = DefaultHypervisor
		}
		for i := range svc.Volumes {
			v := &svc.Volumes[i]
			if v.Type == "share" {
				if v.Mode == "" {
					v.Mode = "rw"
				}
				if v.Protocol == "" {
					v.Protocol = "9p"
				}
			} else if v.Type == "disk" {
				if v.FsType == "" {
					v.FsType = "ext4"
				}
			}
		}
		c.Services[name] = svc
	}
}

func Load(path string) (*Compose, error) {
	return nil, fmt.Errorf("config: not implemented yet")
}
