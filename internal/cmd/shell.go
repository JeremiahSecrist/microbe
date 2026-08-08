package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// newShellCmd is `exec <service>` with no trailing command: ssh opens an
// interactive login shell when given none (incus's `shell` alias is the
// same idea — `exec @ARGS@ -- su -l`, here just riding ssh's own default).
func newShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell <service>",
		Short: "Open an interactive shell in a service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return execRun(execOptions{
				file:    file,
				base:    ".microbe",
				service: args[0],
				stdin:   os.Stdin,
				stdout:  os.Stdout,
				stderr:  os.Stderr,
			})
		},
	}
}
