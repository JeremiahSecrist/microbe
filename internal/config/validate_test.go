package config

import (
	"os"
	"strings"
	"testing"
)

const fixture = "../../test/fixtures/networking/projection.json"

func TestFixtureNetworkingValidates(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Name != "test-net" {
		t.Errorf("name = %q, want test-net", cfg.Name)
	}
	if len(cfg.Networks) != 2 {
		t.Errorf("networks = %d, want 2", len(cfg.Networks))
	}
	if len(cfg.Services) != 3 {
		t.Errorf("services = %d, want 3", len(cfg.Services))
	}
	db := cfg.Services["db"]
	if len(db.Ports) != 1 || db.Ports[0] != "5432:5432" {
		t.Errorf("db ports = %v, want [5432:5432]", db.Ports)
	}
	if db.VCPUs != 1 || db.Mem != 512 {
		t.Errorf("db defaults not applied: vcpu=%d mem=%d", db.VCPUs, db.Mem)
	}
	if db.Hypervisor != "cloud-hypervisor" {
		t.Errorf("db hypervisor default = %q", db.Hypervisor)
	}
	if got := cfg.Services["web"].DependsOn; len(got) != 1 || got[0] != "db" {
		t.Errorf("web dependsOn = %v, want [db]", got)
	}
}

func TestParseAcceptsNetworkShorthand(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "sh",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": { "a": { "networks": ["n"] } }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Services["a"].Networks) != 1 || cfg.Services["a"].Networks[0].Name != "n" {
		t.Errorf("shorthand networks = %+v, want [{n}]", cfg.Services["a"].Networks)
	}
}

func TestValidateUnknownNetwork(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": { "a": { "networks": [{ "name": "missing" }] } }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("want unknown-network error, got %v", err)
	}
}

func TestValidateDuplicateStaticIP(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": {
	    "a": { "networks": [{ "name": "n", "ip": "10.0.0.5" }] },
	    "b": { "networks": [{ "name": "n", "ip": "10.0.0.5" }] }
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("want duplicate-IP error")
	}
}

func TestValidateDependsOnCycle(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": {
	    "a": { "dependsOn": ["b"], "networks": [{ "name": "n" }] },
	    "b": { "dependsOn": ["a"], "networks": [{ "name": "n" }] }
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("want cycle error")
	}
}

func TestValidateHealthcheck(t *testing.T) {
	cases := []struct {
		name    string
		hc      string
		wantErr bool
	}{
		{"valid", `{"port": 5432, "interval": "5s", "timeout": "2s", "startPeriod": "10s"}`, false},
		{"port zero", `{"port": 0, "interval": "5s", "timeout": "2s", "startPeriod": "10s"}`, true},
		{"port too big", `{"port": 70000, "interval": "5s", "timeout": "2s", "startPeriod": "10s"}`, true},
		{"bad interval", `{"port": 5432, "interval": "nah", "timeout": "2s", "startPeriod": "10s"}`, true},
		{"bad timeout", `{"port": 5432, "interval": "5s", "timeout": "nah", "startPeriod": "10s"}`, true},
		{"bad startPeriod", `{"port": 5432, "interval": "5s", "timeout": "2s", "startPeriod": "nah"}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(`{
			  "name": "hc",
			  "networks": { "n": { "subnet": "10.0.0.0/24" } },
			  "services": { "a": { "networks": [{ "name": "n" }], "healthcheck": ` + c.hc + ` } }
			}`))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = cfg.Validate()
			if c.wantErr && err == nil {
				t.Error("want error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("want no error, got %v", err)
			}
		})
	}
}

func TestValidateShareVolumeRequiresName(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": {
	    "a": { "networks": [{ "name": "n" }], "volumes": [ { "type": "share", "host": "/srv", "target": "/data" } ] }
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Errorf("want missing-name error, got %v", err)
	}
}

func TestValidateShareVolumeDuplicateName(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": {
	    "a": { "networks": [{ "name": "n" }], "volumes": [
	      { "type": "share", "name": "data", "host": "/srv/a", "target": "/data" },
	      { "type": "share", "name": "data", "host": "/srv/b", "target": "/other" }
	    ] }
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate share volume name") {
		t.Errorf("want duplicate-share-name error, got %v", err)
	}
}

func TestValidateShareVolumeUnknownProtocol(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": {
	    "a": { "networks": [{ "name": "n" }], "volumes": [
	      { "type": "share", "name": "data", "host": "/srv", "target": "/data", "protocol": "nfs" }
	    ] }
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unknown protocol") {
		t.Errorf("want unknown-protocol error, got %v", err)
	}
}

func TestValidateShareVolumeDefaultsPass(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "ok",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": {
	    "a": { "networks": [{ "name": "n" }], "volumes": [
	      { "name": "data", "host": "/srv", "target": "/data" }
	    ] }
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("want no error for default (share/virtiofs) volume, got %v", err)
	}
}

func TestValidateDiskVolumeRejectsOwner(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": { "n": { "subnet": "10.0.0.0/24" } },
	  "services": {
	    "a": { "networks": [{ "name": "n" }], "volumes": [
	      { "type": "disk", "name": "data", "target": "/data", "size": "2G", "owner": "postgres" }
	    ] }
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "owner only applies to share volumes") {
		t.Errorf("want owner-on-disk error, got %v", err)
	}
}

func TestValidateOverlappingSubnets(t *testing.T) {
	cfg, err := Parse([]byte(`{
	  "name": "bad",
	  "networks": {
	    "a": { "subnet": "10.0.0.0/24" },
	    "b": { "subnet": "10.0.0.128/25" }
	  },
	  "services": {}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("want overlapping-subnet error")
	}
}
