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
		if svc.PID > 0 {
			fmt.Fprintf(opts.out, "stopping %s (pid %d)\n", name, svc.PID)
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
				fmt.Fprintf(opts.out, "warning: %s has no tracked pid but its VM is untracked-live (%s); stop it manually\n", name, chState)
			}
		}
		svc.Status = serviceStatusStopped
		svc.PID = 0
		svc.VirtiofsdPID = 0
		store.Services[name] = svc

		if !opts.dryRun {
			if err := os.RemoveAll(filepath.Join(dataDir, "runs", name)); err != nil {
				return err
			}
		}
	}

	plan, err := hostnet.Plan(cfg)
	if err != nil {
		return err
	}
	st, err := flakegen.FromConfig(cfg, plan)
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

	// Sweep orphaned devices: any interface this stack may have provisioned
	// (recorded in state, or reconstructible from config+state names) that
	// this run's teardown isn't already removing and no staying-up service
	// still needs. Exact-name deletion only — hashed br-*/mvc-* names can't
	// be attributed by prefix, so nothing here is ever guessed from the host.
	planned := map[string]bool{}
	for _, net := range nets {
		planned[hostnet.BridgeName(st.Name, net.Name)] = true
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
			delete(store.Services, name)
			for _, net := range store.Networks {
				delete(net.Allocated, name)
			}
		}
	}

	if !opts.dryRun {
		if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
			return err
		}
	}
	return nil
}
