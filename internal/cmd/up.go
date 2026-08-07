package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up [services...]",
		Short: "Build, provision and start the stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("up: not implemented yet")
			return nil
		},
	}
}
