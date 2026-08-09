package flakegen

import (
	"os"
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

func TestRenderGeneratedOmitsSSHPublicKeyWhenUnset(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "sshPublicKey") {
		t.Errorf("RenderGenerated() should omit sshPublicKey when unset:\n%s", got)
	}
}

func TestRenderGeneratedIncludesSSHPublicKey(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)
	st.SSHPublicKey = "ssh-ed25519 AAAAfake microbe"

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"sshPublicKey": "ssh-ed25519 AAAAfake microbe"`) {
		t.Errorf("RenderGenerated() missing sshPublicKey:\n%s", got)
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
