package flakegen

import (
	"os"
	"testing"
)

func TestRenderFlakeMatchesGolden(t *testing.T) {
	st := mustStack(t, fixtureConfig())
	got := st.RenderFlake()
	want, err := os.ReadFile("testdata/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("RenderFlake():\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestServiceModule(t *testing.T) {
	got := ServiceModule("db")
	want := "{ ... }:\n{\n  microCompose.serviceName = \"db\";\n}\n"
	if got != want {
		t.Errorf("ServiceModule(\"db\") =\n%s\nwant:\n%s", got, want)
	}
}
