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
		Long: `Microbe orchestrates a stack of microvm.nix guests the way docker
compose orchestrates containers: define services, networks and volumes in
one microbe.nix, then bring the whole stack up, inspect it, and tear it
down with a handful of commands. Each service is a real VM (its own
kernel, its own root filesystem) but you drive it like a container.

-f/--file selects which compose file a command operates on (default
microbe.nix in the current directory); --dry-run prints what a command
would do without touching the host or any VM.`,
		Example: `  # scaffold a starter microbe.nix, then bring it up
  microbe init
  microbe up

  # see what's running, then open a shell in one service
  microbe ps
  microbe shell web

  # tear the stack down again
  microbe down --remove-volumes`,
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
	root.AddCommand(newPortForwardCmd())
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
