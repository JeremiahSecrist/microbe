package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build [services...]",
		Short: "Build runner derivations without starting",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("build: not implemented yet")
			return nil
		},
	}
}
