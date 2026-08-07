package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <service> <cmd...>",
		Short: "Run a command inside a service",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("exec: not implemented yet")
			return nil
		},
	}
}
