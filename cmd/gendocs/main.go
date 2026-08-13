// Command gendocs is a standalone doc generator, run via `go run` at Nix
// build time (see flake.nix's microbePkg postInstall and nix/docs.nix's
// cliDocs derivation) -- not shipped as part of the microbe binary itself.
// This is cobra's own documented pattern for doc.GenManTree/GenMarkdownTree:
// they take a *cobra.Command directly and are meant to be called from a
// small external program, not wired into the CLI's own command tree.
package main

import (
	"fmt"
	"os"

	"microbe/internal/cmd"
	"microbe/internal/gendocs"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gendocs <markdown|man> <output-dir>")
		os.Exit(2)
	}
	mode, dir := os.Args[1], os.Args[2]
	root := cmd.NewRootCmd()

	var err error
	switch mode {
	case "markdown":
		err = gendocs.WriteMarkdown(root, dir)
	case "man":
		err = gendocs.WriteMan(root, dir)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q: want markdown or man\n", mode)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}
