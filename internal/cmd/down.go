package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down [services...]",
		Short: "Stop and remove runners",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("down: not implemented yet")
			return nil
		},
	}
}
