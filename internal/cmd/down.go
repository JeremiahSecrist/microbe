package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"microbe/internal/cmdrun"
	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/nix/flakegen"
	"microbe/internal/runtime"
	"microbe/internal/state"
)

func newDownCmd() *cobra.Command {
	var removeVolumes bool
	cmd := &cobra.Command{
		Use:   "down [services...]",
		Short: "Stop services and tear down host resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := cmdrun.Shell()
			if dryRun {
				r = cmdrun.Dry(os.Stdout)
			}
			return downRun(args, downOptions{
				file:          file,
				dryRun:        dryRun,
				removeVolumes: removeVolumes,
				base:          ".microbe",
				runner:        r,
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
	base          string
	runner        cmdrun.Runner
	out           io.Writer
}

func downRun(args []string, o downOptions) error {
	store, err := state.Load(filepath.Join(o.base, "state.json"))
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
			fmt.Fprintf(o.out, "stopping %s (pid %d)\n", name, svc.PID)
			if !o.dryRun {
				if err := stopService(context.Background(), svc.PID, runtime.StopGrace); err != nil {
					return err
				}
			}
		}
		svc.Status = "stopped"
		svc.PID = 0
		store.Services[name] = svc
	}

	if !o.dryRun && geteuid() != 0 {
		return fmt.Errorf("down: host teardown requires root; re-run with sudo")
	}
	cfg, err := config.Load(o.file)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		return err
	}
	st, err := flakegen.FromConfig(cfg, plan)
	if err != nil {
		return err
	}
	nets := netSpecs(st)
	taps := tapSpecs(st)
	ports, err := portSpecs(cfg, st)
	if err != nil {
		return err
	}
	if err := teardownHost(o.runner, st.Name, st, nets, taps, ports); err != nil {
		return err
	}

	if o.removeVolumes {
		for _, name := range selected {
			svc := store.Services[name]
			for _, vol := range svc.Volumes {
				path := runtime.VolumeImagePath(o.base, store.Stack, vol)
				fmt.Fprintf(o.out, "removing volume %s\n", path)
				if !o.dryRun {
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

	if !o.dryRun {
		if err := store.Save(filepath.Join(o.base, "state.json")); err != nil {
			return err
		}
	}
	return nil
}
