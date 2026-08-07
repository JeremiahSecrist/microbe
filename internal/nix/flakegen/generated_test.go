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
	want, err := os.ReadFile("testdata/generated_test-net.nix")
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
	if !strings.Contains(got, `volumes = {`) {
		t.Errorf("RenderGenerated() missing volumes attrset:\n%s", got)
	}
	if !strings.Contains(got, `db-data = {`) {
		t.Errorf("RenderGenerated() missing db-data volume entry:\n%s", got)
	}
	if !strings.Contains(got, `image = "/abs/base/volumes/test-net/db-data.qcow2";`) {
		t.Errorf("RenderGenerated() missing absolute image path:\n%s", got)
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
