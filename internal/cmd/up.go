package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"microbe/internal/cmdrun"
	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/nix/flakegen"
	"microbe/internal/runtime"
)

func newUpCmd() *cobra.Command {
	var noProvision bool
	cmd := &cobra.Command{
		Use:   "up [services...]",
		Short: "Build, provision and start the stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := cmdrun.Shell()
			if dryRun {
				r = cmdrun.Dry(os.Stdout)
			}
			return upRun(args, upOptions{
				file:        file,
				dryRun:      dryRun,
				noProvision: noProvision,
				base:        ".microbe",
				runner:      r,
				out:         os.Stdout,
			})
		},
	}
	cmd.Flags().BoolVar(&noProvision, "no-provision", false, "skip host network provisioning")
	return cmd
}

type upOptions struct {
	file        string
	dryRun      bool
	noProvision bool
	base        string
	runner      cmdrun.Runner
	out         io.Writer
}

func upRun(args []string, o upOptions) error {
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

	if err := flakegen.WriteStack(o.base, st, o.file); err != nil {
		return err
	}
	fmt.Fprintf(o.out, "rendered %s\n", o.base)

	selected := args
	if len(selected) == 0 {
		selected = st.Names()
	}
	for _, svc := range selected {
		if _, ok := st.Services[svc]; !ok {
			return fmt.Errorf("no service %q", svc)
		}
	}

	for _, svc := range selected {
		outLink := filepath.Join(o.base, "runners", svc)
		if o.dryRun {
			fmt.Fprintf(o.out, "nix build .#nixosConfigurations.%s.config.microvm.declaredRunner -> %s\n", svc, outLink)
			continue
		}
		path, err := buildRunner(o.base, svc, outLink)
		if err != nil {
			return err
		}
		fmt.Fprintf(o.out, "%s -> %s\n", svc, path)
	}

	if !o.noProvision {
		if !o.dryRun && geteuid() != 0 {
			return fmt.Errorf("up: host provisioning requires root; re-run with sudo or pass --no-provision")
		}
		nets := netSpecs(st)
		taps := tapSpecs(st)
		ports, err := portSpecs(cfg, st)
		if err != nil {
			return err
		}
		if err := provisionHost(o.runner, st.Name, st, nets, taps, ports); err != nil {
			return err
		}
	}

	order, err := startOrder(cfg, selected)
	if err != nil {
		return err
	}

	pids := map[string]int{}
	for _, svc := range order {
		if o.dryRun {
			fmt.Fprintf(o.out, "start %s\n", svc)
			continue
		}
		for _, v := range cfg.Services[svc].Volumes {
			if v.Type != "disk" {
				continue
			}
			path, err := runtime.EnsureVolume(o.runner, o.base, cfg.Name, v.Name, v.Size)
			if err != nil {
				return err
			}
			fmt.Fprintf(o.out, "volume %s\n", path)
		}
		pid, err := startService(context.Background(),
			filepath.Join(o.base, "runners", svc),
			filepath.Join(o.base, "runs", svc),
			filepath.Join(o.base, "logs", svc+".log"))
		if err != nil {
			return err
		}
		pids[svc] = pid
		fmt.Fprintf(o.out, "started %s (pid %d)\n", svc, pid)
	}

	if !o.dryRun {
		store := buildStore(cfg, st, pids, filepath.Join(o.base, "runners"))
		if err := store.Save(filepath.Join(o.base, "state.json")); err != nil {
			return err
		}
		printStore(o.out, store)
	}
	return nil
}
