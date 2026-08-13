package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestNewRootCmdExposesSubcommands(t *testing.T) {
	root := NewRootCmd()
	if root.Use != "microbe" {
		t.Fatalf("root.Use = %q, want %q", root.Use, "microbe")
	}
	want := []string{"up", "down", "purge"}
	for _, name := range want {
		found := false
		for _, c := range root.Commands() {
			if c.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("root command missing subcommand %q", name)
		}
	}
}

func TestGendocsCommandIsHidden(t *testing.T) {
	root := NewRootCmd()
	gendocs, _, err := root.Find([]string{"gendocs", "man"})
	if err != nil {
		t.Fatalf("expected a hidden gendocs man subcommand: %v", err)
	}
	if !gendocs.Hidden {
		t.Errorf("gendocs man should be hidden from --help, like provisiond")
	}
	if gendocs.Args == nil {
		t.Errorf("gendocs man should require exactly one dir argument")
	}
}

func TestAllVisibleCommandsHaveLongDescription(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if !c.Hidden && c.Long == "" {
			t.Errorf("command %q has no Long description", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	for _, c := range NewRootCmd().Commands() {
		walk(c)
	}
}
