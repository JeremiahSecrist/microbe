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
	if db.VCpus != 1 || db.Mem != 512 {
		t.Errorf("db defaults not applied: vcpu=%d mem=%d", db.VCpus, db.Mem)
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
