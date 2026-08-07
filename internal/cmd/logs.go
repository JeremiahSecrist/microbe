package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs [services...]",
		Short: "Show guest logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("logs: not implemented yet")
			return nil
		},
	}
}
