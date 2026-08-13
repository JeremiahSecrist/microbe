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
