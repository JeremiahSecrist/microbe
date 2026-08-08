package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"

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
	reconcileVMState(base, store)
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		return err
	}
	printStore(out, store)
	return nil
}

// reconcileVMState asks cloud-hypervisor's own API socket for each live
// service's actual state, correcting store in place: microbe's own PID
// bookkeeping doesn't notice a VMM that crashed or was killed out from
// under it.
func reconcileVMState(base string, store *state.Store) {
	for name, svc := range store.Services {
		if svc.PID == 0 {
			continue
		}
		chState, err := vmState(vmSocketPath(base, name))
		switch {
		case err != nil:
			// Socket file present but unreachable: the VMM died without
			// cleaning up after itself.
			svc.Status, svc.PID = serviceStatusCrashed, 0
		case chState == "":
			// Socket gone entirely: fully torn down.
			svc.Status, svc.PID = serviceStatusStopped, 0
		case chState != "Running":
			svc.Status = strings.ToLower(chState)
		}
		store.Services[name] = svc
	}
}
