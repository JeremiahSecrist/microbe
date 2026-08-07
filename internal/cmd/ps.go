package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List services, status, IPs and ports",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("ps: not implemented yet")
			return nil
		},
	}
}
