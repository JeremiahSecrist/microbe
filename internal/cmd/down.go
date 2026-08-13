package cmd

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/spf13/cobra"

	"microbe/internal/cmdrun"
	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/hostnet"
	"microbe/internal/nix/flakegen"
	"microbe/internal/provisiond"
	"microbe/internal/runtime"
	"microbe/internal/state"
)

func newDownCmd() *cobra.Command {
	var removeVolumes bool
	cmd := &cobra.Command{
		Use:   "down [services...]",
		Short: "Stop services and tear down host resources",
		Long: `Down stops the selected services (all of them if none are named),
tears down the host networking (bridges/taps/published ports) that up
provisioned for them, and sweeps any now-orphaned devices. Pass
--remove-volumes to also delete their disk images and clear their state.`,
		Example: `  # stop everything; keep disks and provisioned networking state
  microbe down

  # stop just one service
  microbe down web

  # stop and wipe disks/state entirely
  microbe down --remove-volumes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := cmdrun.Shell()
			if dryRun {
				runner = cmdrun.Dry(os.Stdout)
			}
			var ops provisiond.Ops
			if dryRun {
				ops = printOps{out: os.Stdout}
			} else {
				conn, err := provisiond.Dial(provisiond.SocketPath)
				if err != nil {
					return err
				}
				defer conn.Close()
				ops = conn
			}
			return downRun(args, downOptions{
				file:          file,
				dryRun:        dryRun,
				removeVolumes: removeVolumes,
				runner:        runner,
				ops:           ops,
				out:           os.Stdout,
			})
		},
	}
	cmd.Flags().BoolVar(&removeVolumes, "remove-volumes", false, "delete disk images and clear service state")
	return cmd
}

type downOptions struct {
	file          string
	dryRun        bool
	removeVolumes bool
	runner        cmdrun.Runner
	ops           provisiond.Ops
	out           io.Writer
}

func downRun(args []string, opts downOptions) error {
	cfg, err := config.Load(opts.file)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	dataDir := datadir.Dir(cfg.Name)

	store, err := state.Load(filepath.Join(dataDir, "state.json"))
	if err != nil {
		return err
	}

	selected := args
	if len(selected) == 0 {
		for name := range store.Services {
			selected = append(selected, name)
		}
		sort.Strings(selected)
	}

	for _, name := range selected {
		svc, ok := store.Services[name]
		if !ok {
			return fmt.Errorf("no service %q in state", name)
		}
		label := name
		if svc.Stale {
			label = name + " (removed from config)"
		}
		if svc.PID > 0 {
			fmt.Fprintf(opts.out, "stopping %s (pid %d)\n", label, svc.PID)
			if !opts.dryRun {
				if err := stopService(context.Background(), svc.PID, runtime.StopGrace); err != nil {
					return err
				}
				if svc.VirtiofsdPID > 0 {
					if err := stopService(context.Background(), svc.VirtiofsdPID, runtime.StopGrace); err != nil {
						return err
					}
				}
			}
		} else if !opts.dryRun {
			// state.json may have lost track of this service's PID (e.g. a
			// prior partial `up` that never recorded it) while its VM is
			// still actually alive. There's no PID to stop by, but this is
			// worth surfacing instead of silently claiming a clean stop.
			if chState, err := vmState(vmSocketPath(dataDir, name)); err == nil && chState != "" {
				fmt.Fprintf(opts.out, "warning: %s has no tracked pid but its VM is untracked-live (%s); stop it manually\n", label, chState)
			}
		}

		if !opts.dryRun {
			if err := cleanRunDir(filepath.Join(dataDir, "runs", name)); err != nil {
				return err
			}
		}

		if svc.Stale {
			// Leave the entry (and its Volumes, needed below if
			// --remove-volumes was given) in place for now; it's swept from
			// state once we're done with it below. Deleting it here would
			// make the --remove-volumes pass below unable to find its
			// volumes to clean up.
			continue
		}
		svc.Status = serviceStatusStopped
		svc.PID = 0
		svc.VirtiofsdPID = 0
		store.Services[name] = svc
	}

	plan, prefix, err := resolvePlan(cfg, opts.file, opts.ops)
	if err != nil {
		return err
	}
	st, err := flakegen.FromConfig(cfg, plan, prefix)
	if err != nil {
		return err
	}
	nets := netSpecsForTeardown(st, selected)
	taps := tapSpecs(cfg, st, selected)
	ports, err := portSpecs(cfg, st, selected)
	if err != nil {
		return err
	}
	if err := teardownHost(opts.ops, st.Name, nets, taps, ports); err != nil {
		return err
	}
	if err := opts.ops.TeardownRules(ruleSpecs(cfg, st)); err != nil {
		return err
	}

	// Sweep orphaned devices: any interface this stack may have provisioned
	// (recorded in state, or reconstructible from config+state names) that
	// this run's teardown isn't already removing and no staying-up service
	// still needs. Exact-name deletion only — hashed br-*/mvc-* names can't
	// be attributed by prefix, so nothing here is ever guessed from the host.
	planned := map[string]bool{}
	for range nets {
		planned[hostnet.BridgeName(st.Name)] = true
	}
	for _, tap := range taps {
		planned[tap.Name] = true
	}
	retained := map[string]bool{}
	for _, name := range retainedDeviceNames(cfg, st, store, selected) {
		retained[name] = true
	}
	candidates := append(stackDeviceNames(cfg, st, store), store.Provisioned...)
	var orphans []string
	for _, name := range dedupeNames(candidates) {
		if !planned[name] && !retained[name] {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		if err := sweepOrphanLinks(opts.ops, orphans); err != nil {
			return err
		}
		for _, name := range orphans {
			fmt.Fprintf(opts.out, "sweeping orphaned link %s\n", name)
		}
	}
	if !opts.dryRun {
		// After teardown + sweep, the only devices left owned are the ones
		// non-selected services still need.
		store.Provisioned = dedupeNames(slices.Collect(maps.Keys(retained)))
	}

	if opts.removeVolumes {
		for _, name := range selected {
			svc := store.Services[name]
			for _, vol := range svc.Volumes {
				path := runtime.VolumeImagePath(dataDir, vol)
				fmt.Fprintf(opts.out, "removing volume %s\n", path)
				if !opts.dryRun {
					if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
						return err
					}
				}
			}
			if !opts.dryRun {
				if err := removeSnapshots(filepath.Join(dataDir, "runs", name)); err != nil {
					return err
				}
			}
			delete(store.Services, name)
		}
	}

	// No config names a Stale entry anymore, so there's nothing for a future
	// up to reuse it for; keeping it around would just accumulate dead
	// weight in state.json. This runs last (after any --remove-volumes pass
	// above, which needed the entry's Volumes still present) so it also
	// catches Stale entries --remove-volumes didn't already delete.
	for _, name := range selected {
		svc, ok := store.Services[name]
		if !ok || !svc.Stale {
			continue
		}
		delete(store.Services, name)
	}

	if !opts.dryRun {
		if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
			return err
		}
	}
	return nil
}
