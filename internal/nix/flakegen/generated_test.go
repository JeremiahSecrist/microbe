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

// TestRenderGeneratedOmitsShareOwnerHostIDs proves host-specific uid/gid are
// NOT written to generated.json. They used to be, but were moved to runtime
// env vars (MICROBE_HOST_UID_<TAG> / MICROBE_HOST_GID_<TAG>) so the nix
// derivation hash is host-independent. ShareOwners is still populated by
// up.go and passed to virtiofsd via virtiofsdEnv, but never touches the file.
func TestRenderGeneratedOmitsShareOwnerHostIDs(t *testing.T) {
	cfg := fixtureConfig()
	st := mustStack(t, cfg)

	db := st.Services["db"]
	db.ShareOwners = map[string]ShareOwner{"db-data": {HostUID: 1000, HostGID: 100}}
	st.Services["db"] = db

	got, err := st.RenderGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `"hostUid"`) {
		t.Errorf("RenderGenerated() must not emit hostUid (host-specific; goes to env var instead):\n%s", got)
	}
	if strings.Contains(got, `"hostGid"`) {
		t.Errorf("RenderGenerated() must not emit hostGid (host-specific; goes to env var instead):\n%s", got)
	}
	if strings.Contains(got, `"host"`) {
		t.Errorf("RenderGenerated() must not emit host path for share volumes (host-specific; goes to env var instead):\n%s", got)
	}
}

// TestRenderGeneratedOmitsShareHostPaths proves ShareHosts host paths are not
// written to generated.json either. Both ShareHosts and ShareOwners data flows
// to virtiofsd at runtime via MICROBE_SHARE_<TAG> / MICROBE_HOST_UID_<TAG>
// env vars (see virtiofsdEnv in up.go), never through the generated file.
func TestRenderGeneratedOmitsShareHostPaths(t *testing.T) {
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
	if strings.Contains(got, `/var/lib/microbe/test-net/volumes/db-data`) {
		t.Errorf("RenderGenerated() must not emit share host path (host-specific; goes to env var instead):\n%s", got)
	}
	if strings.Contains(got, `"hostUid"`) || strings.Contains(got, `"hostGid"`) {
		t.Errorf("RenderGenerated() must not emit hostUid/hostGid:\n%s", got)
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

func mustStack(t *testing.T, cfg *config.Compose) *Stack {
	t.Helper()
	plan, err := hostnet.Plan(cfg, newTestLock())
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan, testPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
