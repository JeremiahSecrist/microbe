package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the evaluated and validated config",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("config: not implemented yet")
			return nil
		},
	}
}
