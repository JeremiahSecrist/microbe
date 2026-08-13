package cmd

import (
	"github.com/spf13/cobra"

	"microbe/internal/gendocs"
)

// newGendocsCmd returns the hidden `microbe gendocs` command tree, run at
// Nix build time (see flake.nix's microbePkg postInstall and nix/docs.nix)
// to render this same command tree as man pages and CLI reference markdown.
// Hidden like provisiond: an internal build tool, not something an end user
// runs directly.
func newGendocsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "gendocs",
		Short:  "Generate CLI docs (internal build tool)",
		Hidden: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use:    "markdown <dir>",
		Short:  "Write CLI reference markdown into <dir>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return gendocs.WriteMarkdown(c.Root(), args[0])
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:    "man <dir>",
		Short:  "Write section-1 man pages into <dir>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return gendocs.WriteMan(c.Root(), args[0])
		},
	})
	return cmd
}
