// Command gendocs renders the microbe CLI reference as markdown or man
// pages, from the same command tree the CLI itself builds. Used at Nix
// build time by nix/docs.nix and nix/man.nix; see internal/gendocs.
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
