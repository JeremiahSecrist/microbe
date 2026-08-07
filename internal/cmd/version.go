package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "0.0.0"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("microbe", version)
		},
	}
}
