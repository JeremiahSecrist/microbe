// Package gendocs generates the CLI markdown reference and man pages for
// the microbe command tree, both from the same *cobra.Command built by
// cmd.NewRootCmd -- keeping the docs in sync with the CLI is then just a
// matter of regenerating from that tree instead of hand-maintaining prose.
package gendocs

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// WriteMarkdown renders root and every subcommand as a page of markdown
// into dir, one file per command (doc.GenMarkdownTree's naming: microbe.md,
// microbe_up.md, ...).
func WriteMarkdown(root *cobra.Command, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return doc.GenMarkdownTree(root, dir)
}

// WriteMan renders root and every subcommand as a section-1 man page into
// dir (doc.GenManTree's naming: microbe.1, microbe-up.1, ...).
func WriteMan(root *cobra.Command, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	header := &doc.GenManHeader{Title: "MICROBE", Section: "1"}
	return doc.GenManTree(root, header, dir)
}
