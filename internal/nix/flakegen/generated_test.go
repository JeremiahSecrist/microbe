package flakegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"microbe/internal/config"
	"microbe/internal/hostnet"
)

func TestRenderGeneratedMatchesGolden(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/generated_test-net.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("RenderGenerated():\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderGeneratedIncludesAbsoluteVolumeImage(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)

	db := st.Services["db"]
	db.VolumeImages = map[string]string{"db-data": "/abs/base/volumes/test-net/db-data.qcow2"}
	st.Services["db"] = db

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"volumes": {`) {
		t.Errorf("RenderGenerated() missing volumes object:\n%s", got)
	}
	if !strings.Contains(got, `"db-data": {`) {
		t.Errorf("RenderGenerated() missing db-data volume entry:\n%s", got)
	}
	if !strings.Contains(got, `"image": "/abs/base/volumes/test-net/db-data.qcow2"`) {
		t.Errorf("RenderGenerated() missing absolute image path:\n%s", got)
	}
}

func TestRenderGeneratedIncludesShareOwnerHostIDs(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)

	db := st.Services["db"]
	db.ShareOwners = map[string]ShareOwner{"db-data": {HostUID: 1000, HostGID: 100}}
	st.Services["db"] = db

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"volumes": {`) {
		t.Errorf("RenderGenerated() missing volumes object:\n%s", got)
	}
	if !strings.Contains(got, `"db-data": {`) {
		t.Errorf("RenderGenerated() missing db-data volume entry:\n%s", got)
	}
	if !strings.Contains(got, `"hostUid": 1000`) {
		t.Errorf("RenderGenerated() missing hostUid:\n%s", got)
	}
	if !strings.Contains(got, `"hostGid": 100`) {
		t.Errorf("RenderGenerated() missing hostGid:\n%s", got)
	}
}

// TestRenderGeneratedIncludesShareHostAndMergesWithOwner proves ShareHosts
// renders alongside ShareOwners under the same volume entry instead of one
// clobbering the other -- a share volume can have both a defaulted host
// (ShareHosts) and an owner translation (ShareOwners) at once.
func TestRenderGeneratedIncludesShareHostAndMergesWithOwner(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)

	db := st.Services["db"]
	db.ShareHosts = map[string]string{"db-data": "/var/lib/microbe/test-net/volumes/db-data"}
	db.ShareOwners = map[string]ShareOwner{"db-data": {HostUID: 1000, HostGID: 100}}
	st.Services["db"] = db

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"host": "/var/lib/microbe/test-net/volumes/db-data"`) {
		t.Errorf("RenderGenerated() missing default host:\n%s", got)
	}
	if !strings.Contains(got, `"hostUid": 1000`) {
		t.Errorf("RenderGenerated() lost hostUid when merged with host:\n%s", got)
	}
	if !strings.Contains(got, `"hostGid": 100`) {
		t.Errorf("RenderGenerated() lost hostGid when merged with host:\n%s", got)
	}
}

func TestRenderGeneratedIncludesBuildTarget(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Services["db"] = config.Service{OS: "finix", Networks: cfg.Services["db"].Networks}
	st := mustStack(t, cfg)

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"buildTarget": ".#finixConfigurations.db.config.microbe.qemuRunner"`) {
		t.Errorf("RenderGenerated() missing finix buildTarget for db:\n%s", got)
	}
	if !strings.Contains(got, `"buildTarget": ".#nixosConfigurations.web.config.microvm.declaredRunner"`) {
		t.Errorf("RenderGenerated() missing nixos buildTarget for web:\n%s", got)
	}
}

func TestLoadGeneratedCID(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)
	generated, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "generated.json"), []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadGeneratedCID(dir, "db")
	if err != nil {
		t.Fatal(err)
	}
	if want := st.Services["db"].CID; got != want {
		t.Errorf("LoadGeneratedCID() = %d, want %d", got, want)
	}
}

func TestLoadGeneratedCIDUnknownService(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)
	generated, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "generated.json"), []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadGeneratedCID(dir, "nope"); err == nil {
		t.Error("LoadGeneratedCID() for unknown service = nil error, want error")
	}
}

func TestLoadGeneratedCIDMissingFile(t *testing.T) {
	if _, err := LoadGeneratedCID(t.TempDir(), "db"); err == nil {
		t.Error("LoadGeneratedCID() with no generated.json = nil error, want error")
	}
}

func mustStack(t *testing.T, cfg *config.Compose) *Stack {
	t.Helper()
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
