package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs [services...]",
		Short: "Show guest logs",
		Long:  `Logs prints guest service logs. Not yet implemented.`,
		Example: `  # all services' logs
  microbe logs

  # just one
  microbe logs web`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("logs: not implemented yet")
			return nil
		},
	}
}
