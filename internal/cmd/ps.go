package cmd

import (
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"microbe/internal/state"
)

func newPsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List services, status, IPs and ports",
		RunE: func(cmd *cobra.Command, args []string) error {
			return psRun(".microbe", os.Stdout)
		},
	}
}

func psRun(base string, out io.Writer) error {
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		return err
	}
	printStore(out, store)
	return nil
}
