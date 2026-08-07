package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm [services...]",
		Short: "Remove disks and state",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("rm: not implemented yet")
			return nil
		},
	}
}
