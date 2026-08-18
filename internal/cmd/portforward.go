package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"microbe/internal/portproxy"
)

// newPortForwardCmd returns the hidden _portforward subcommand used by
// StartPortForwarder to exec a detached child that runs the proxy loop.
func newPortForwardCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_portforward <listen-addr> <dial-addr>",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := portproxy.Serve(args[0], args[1]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return err
			}
			return nil
		},
	}
}
