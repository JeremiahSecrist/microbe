package flakegen

import (
	"os"
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
