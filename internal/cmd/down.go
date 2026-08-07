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
				base:          ".microbe",
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
	base          string
	runner        cmdrun.Runner
	ops           provisiond.Ops
	out           io.Writer
}

func downRun(args []string, opts downOptions) error {
	store, err := state.Load(filepath.Join(opts.base, "state.json"))
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
			}
		}
		svc.Status = serviceStatusStopped
		svc.PID = 0
		store.Services[name] = svc
	}

	cfg, err := config.Load(opts.file)
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
	if err := teardownHost(opts.ops, st.Name, nets, taps, ports); err != nil {
		return err
	}

	if opts.removeVolumes {
		for _, name := range selected {
			svc := store.Services[name]
			for _, vol := range svc.Volumes {
				path := runtime.VolumeImagePath(opts.base, store.Stack, vol)
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
		if err := store.Save(filepath.Join(opts.base, "state.json")); err != nil {
			return err
		}
	}
	return nil
}
