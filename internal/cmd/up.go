package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

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
			runner := cmdrun.Shell()
			if dryRun {
				runner = cmdrun.Dry(os.Stdout)
			}
			var ops provisiond.Ops
			if dryRun {
				ops = printOps{out: os.Stdout}
			} else if !noProvision {
				conn, err := provisiond.Dial(provisiond.SocketPath)
				if err != nil {
					return err
				}
				defer conn.Close()
				ops = conn
			}
			return upRun(args, upOptions{
				file:        file,
				dryRun:      dryRun,
				noProvision: noProvision,
				base:        ".microbe",
				runner:      runner,
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
// on-host qcow2 path (runtime.VolumeImagePath is relative to base), so
// generated.nix carries a path renderer.nix can use regardless of the
// runner's CWD.
func attachVolumeImages(base string, cfg *config.Compose, st *flakegen.Stack) error {
	for name, svcCfg := range cfg.Services {
		svc, ok := st.Services[name]
		if !ok {
			continue
		}
		for _, vol := range svcCfg.Volumes {
			if vol.Type != volumeTypeDisk {
				continue
			}
			abs, err := filepath.Abs(runtime.VolumeImagePath(base, cfg.Name, vol.Name))
			if err != nil {
				return err
			}
			if svc.VolumeImages == nil {
				svc.VolumeImages = map[string]string{}
			}
			svc.VolumeImages[vol.Name] = abs
		}
		st.Services[name] = svc
	}
	return nil
}

// defaultVolumeFsType is applied to disk volumes that don't specify one.
const defaultVolumeFsType = "ext4"

func upRun(args []string, opts upOptions) error {
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
	if err := attachVolumeImages(opts.base, cfg, st); err != nil {
		return err
	}

	if !opts.dryRun {
		// Always the real shell runner: key generation is independent of
		// opts.runner (which tests fake out for qemu-img/mkfs volume
		// commands) and needs ssh-keygen to actually run.
		_, pub, err := ensureSSHKeypair(cmdrun.Shell(), filepath.Join(opts.base, "ssh"))
		if err != nil {
			return err
		}
		st.SSHPublicKey = pub
	}

	if err := flakegen.WriteStack(opts.base, st, opts.file); err != nil {
		return err
	}
	fmt.Fprintf(opts.out, "rendered %s\n", opts.base)

	selected := args
	if len(selected) == 0 {
		selected = st.Names()
	}
	for _, svc := range selected {
		if _, ok := st.Services[svc]; !ok {
			return fmt.Errorf("no service %q", svc)
		}
	}

	if opts.dryRun {
		for _, svc := range selected {
			outLink := filepath.Join(opts.base, "runners", svc)
			fmt.Fprintf(opts.out, "nix build .#nixosConfigurations.%s.config.microvm.declaredRunner -> %s\n", svc, outLink)
		}
	} else {
		// Each service's derivation is independent, so build them concurrently
		// instead of paying N sequential `nix build` round trips; nix's own
		// daemon serializes/parallelizes the underlying store realizations.
		paths := make([]string, len(selected))
		var g errgroup.Group
		for i, svc := range selected {
			i, svc := i, svc
			g.Go(func() error {
				path, err := buildRunner(opts.base, svc, filepath.Join(opts.base, "runners", svc))
				if err != nil {
					return err
				}
				paths[i] = path
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
		for i, svc := range selected {
			fmt.Fprintf(opts.out, "%s -> %s\n", svc, paths[i])
		}
	}

	if !opts.noProvision {
		nets := netSpecs(st)
		taps := tapSpecs(cfg, st, selected)
		ports, err := portSpecs(cfg, st, selected)
		if err != nil {
			return err
		}
		if err := provisionHost(opts.ops, st.Name, nets, taps, ports); err != nil {
			return err
		}
	}

	order, err := startOrder(cfg, selected)
	if err != nil {
		return err
	}

	pids := map[string]int{}
	statuses := map[string]string{}
	var healthErr error
	for _, svc := range order {
		if opts.dryRun {
			fmt.Fprintf(opts.out, "start %s\n", svc)
			continue
		}
		for _, vol := range cfg.Services[svc].Volumes {
			if vol.Type != volumeTypeDisk {
				continue
			}
			fsType := vol.FsType
			if fsType == "" {
				fsType = defaultVolumeFsType
			}
			path, err := runtime.EnsureVolume(opts.runner, opts.base, cfg.Name, vol.Name, vol.Size, fsType)
			if err != nil {
				return err
			}
			fmt.Fprintf(opts.out, "volume %s\n", path)
		}
		pid, err := startService(context.Background(),
			filepath.Join(opts.base, "runners", svc),
			filepath.Join(opts.base, "runs", svc),
			filepath.Join(opts.base, "logs", svc+".log"))
		if err != nil {
			return err
		}
		pids[svc] = pid
		fmt.Fprintf(opts.out, "started %s (pid %d)\n", svc, pid)

		if hc := cfg.Services[svc].Healthcheck; hc != nil {
			ip := st.Services[svc].IPs[primaryNetwork(st.Services[svc])]
			healthy, err := probeHealth(*hc, ip)
			if err != nil {
				return err
			}
			if healthy {
				statuses[svc] = serviceStatusHealthy
				fmt.Fprintf(opts.out, "healthy %s\n", svc)
			} else {
				statuses[svc] = serviceStatusDegraded
				healthErr = fmt.Errorf("service %q did not become healthy within %s", svc, hc.StartPeriod)
				fmt.Fprintf(opts.out, "degraded %s: %v\n", svc, healthErr)
				// Stop the VM we just started: leaving it running would let
				// a follow-up `up` race a second instance against it over
				// the same microvm API socket/tap.
				if err := stopService(context.Background(), pid, runtime.StopGrace); err != nil {
					fmt.Fprintf(opts.out, "warning: failed to stop degraded %s (pid %d): %v\n", svc, pid, err)
				} else {
					pids[svc] = 0
				}
				break
			}
		}
	}

	if !opts.dryRun {
		store := buildStore(cfg, st, pids, statuses, filepath.Join(opts.base, "runners"))
		if err := store.Save(filepath.Join(opts.base, "state.json")); err != nil {
			return err
		}
		printStore(opts.out, store)
	}
	if healthErr != nil {
		return healthErr
	}
	return nil
}
