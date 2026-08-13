package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/state"
)

func newPsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List services, status, IPs and ports",
		Long: `Ps reconciles the stack's recorded state against each running VM's own
cloud-hypervisor API socket (correcting for VMs that crashed or were killed
outside microbe), saves the reconciled state, then prints each service's
status, IPs and published ports.`,
		Example: `  # list every service in the current stack: status, IPs, ports
  microbe ps`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return psRun(file, os.Stdout)
		},
	}
}

func psRun(configFile string, out io.Writer) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}
	base := datadir.Dir(cfg.Name)
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		return err
	}
	reconcileVMState(base, store)
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		return err
	}
	printStore(out, store, isTerminal(out))
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
