package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	file    string
	verbose bool
	dryRun  bool
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "microbe",
		Short: "Docker-compose-style orchestration for microvm.nix",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if verbose {
				fmt.Println("verbose mode enabled")
			}
		},
	}
	root.PersistentFlags().StringVarP(&file, "file", "f", "microbe.nix", "compose file path")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	root.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "print actions without executing")

	root.AddCommand(newInitCmd())
	root.AddCommand(newUpCmd())
	root.AddCommand(newDownCmd())
	root.AddCommand(newPsCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newExecCmd())
	root.AddCommand(newShellCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newBuildCmd())
	root.AddCommand(newRmCmd())
	root.AddCommand(newPurgeCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newProvisiondCmd())
	root.AddCommand(newGendocsCmd())
	return root
}

// Execute runs the microbe CLI: it builds the root command tree and parses
// os.Args, returning any error the invoked subcommand reports.
func Execute() error {
	return newRootCmd().Execute()
}

// NewRootCmd builds the microbe command tree for callers outside this
// package (currently: the doc/man-page generator in internal/gendocs).
func NewRootCmd() *cobra.Command {
	return newRootCmd()
}
