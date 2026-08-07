package config

import (
	"os/exec"
	"testing"
)

func TestEvalFixtureProjection(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not available")
	}
	cfg, err := Load("../../test/fixtures/networking/microbe.nix")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Name != "test-net" {
		t.Errorf("name = %q, want test-net", cfg.Name)
	}
	if len(cfg.Services) != 3 {
		t.Errorf("services = %d, want 3", len(cfg.Services))
	}
	db := cfg.Services["db"]
	if !db.ConfigPresent {
		t.Error("db configPresent should be true")
	}
	if len(db.Volumes) != 1 || db.Volumes[0].Name != "db-data" {
		t.Errorf("db volumes = %+v", db.Volumes)
	}
	if got := cfg.Services["web"].DependsOn; len(got) != 1 || got[0] != "db" {
		t.Errorf("web dependsOn = %v, want [db]", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}
