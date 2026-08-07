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
	"microbe/internal/provisiond"
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
			var ops provisiond.Ops
			if dryRun {
				ops = printOps{out: os.Stdout}
			} else if !noProvision {
				c, err := provisiond.Dial(provisiond.SocketPath)
				if err != nil {
					return err
				}
				defer c.Close()
				ops = c
			}
			return upRun(args, upOptions{
				file:        file,
				dryRun:      dryRun,
				noProvision: noProvision,
				base:        ".microbe",
				runner:      r,
				ops:         ops,
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
	ops         provisiond.Ops
	out         io.Writer
}

// attachVolumeImages populates each service's disk volumes with the absolute
// on-host qcow2 path (runtime.VolumeImagePath is relative to o.base), so
// generated.nix carries a path renderer.nix can use regardless of the
// runner's CWD.
func attachVolumeImages(base string, cfg *config.Compose, st *flakegen.Stack) error {
	for name, svcCfg := range cfg.Services {
		s, ok := st.Services[name]
		if !ok {
			continue
		}
		for _, v := range svcCfg.Volumes {
			if v.Type != "disk" {
				continue
			}
			abs, err := filepath.Abs(runtime.VolumeImagePath(base, cfg.Name, v.Name))
			if err != nil {
				return err
			}
			if s.VolumeImages == nil {
				s.VolumeImages = map[string]string{}
			}
			s.VolumeImages[v.Name] = abs
		}
		st.Services[name] = s
	}
	return nil
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
	if err := attachVolumeImages(o.base, cfg, st); err != nil {
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
		nets := netSpecs(st)
		taps := tapSpecs(st)
		ports, err := portSpecs(cfg, st)
		if err != nil {
			return err
		}
		if err := provisionHost(o.ops, st.Name, st, nets, taps, ports); err != nil {
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
			fsType := v.FsType
			if fsType == "" {
				fsType = "ext4"
			}
			path, err := runtime.EnsureVolume(o.runner, o.base, cfg.Name, v.Name, v.Size, fsType)
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
