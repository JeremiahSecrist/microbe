package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"microbe/internal/cmdrun"
	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/hostnet"
	"microbe/internal/nix/flakegen"
	"microbe/internal/provisiond"
	"microbe/internal/runtime"
	"microbe/internal/state"
)

func newUpCmd() *cobra.Command {
	var noProvision bool
	cmd := &cobra.Command{
		Use:   "up [services...]",
		Short: "Build, provision and start the stack",
		Long: `Up renders the stack from microbe.nix (-f/--file), builds each selected
service's runner derivation (concurrently), provisions host networking
(bridges, taps, published ports) unless --no-provision is given, then starts
each service in dependency order. Services with a healthcheck are waited on
and stopped again if they don't become healthy within its startPeriod. With
no service names given, every service in the stack is started.`,
		Example: `  # build, provision and start every service in the stack
  microbe up

  # just these two
  microbe up web db

  # see what would happen without touching the nix store or the host
  microbe --dry-run up

  # start without touching host networking (bridges/taps/ports)
  microbe up --no-provision`,
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
	runner      cmdrun.Runner
	ops         provisiond.Ops
	out         io.Writer
}

// attachVolumeImages populates each service's disk volumes with the absolute
// on-host qcow2 path (runtime.VolumeImagePath is relative to dataDir), so
// generated.json carries a path renderer.nix can use regardless of the
// runner's CWD.
func attachVolumeImages(dataDir string, cfg *config.Compose, st *flakegen.Stack) error {
	for name, svcCfg := range cfg.Services {
		svc, ok := st.Services[name]
		if !ok {
			continue
		}
		for _, vol := range svcCfg.Volumes {
			if vol.Type != volumeTypeDisk {
				continue
			}
			abs, err := filepath.Abs(runtime.VolumeImagePath(dataDir, vol.Name))
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

// attachShareOwners populates each service's owner-translated share volumes
// with their host directory's actual owning uid/gid, so renderer.nix can
// pass it to virtiofsd's --translate-uid/--translate-gid (see
// flakegen.ShareOwner). Only Go can determine this — the guest side of the
// mapping is resolved entirely in renderer.nix from the guest's own user
// database.
func attachShareOwners(cfg *config.Compose, st *flakegen.Stack) error {
	for name, svcCfg := range cfg.Services {
		svc, ok := st.Services[name]
		if !ok {
			continue
		}
		for _, vol := range svcCfg.Volumes {
			if vol.Type != "share" || vol.Owner == "" {
				continue
			}
			fi, err := os.Stat(vol.Host)
			if err != nil {
				return fmt.Errorf("service %q: share %q: stat host path for owner translation: %w", name, vol.Name, err)
			}
			stat := fi.Sys().(*syscall.Stat_t)
			if svc.ShareOwners == nil {
				svc.ShareOwners = map[string]flakegen.ShareOwner{}
			}
			svc.ShareOwners[vol.Name] = flakegen.ShareOwner{HostUID: int(stat.Uid), HostGID: int(stat.Gid)}
		}
		st.Services[name] = svc
	}
	return nil
}

// attachShareHosts resolves every share volume's host path to an absolute
// one and records it on cfg (so attachShareOwners's stat has somewhere to
// look) and on st.ShareHosts: renderer.nix imports the user's raw
// microbe.nix directly rather than Go's resolved cfg, so it always reads
// the host path through generated.json instead of the (possibly relative)
// value written in microbe.nix (see flakegen.Service.ShareHosts).
//
// A share that omits host gets a docker-style managed directory
// (<dataDir>/volumes/<name>), created if it doesn't exist yet. A relative
// host is resolved against projectDir (the directory containing
// microbe.nix), so users can write paths like "./data" instead of having
// to hardcode an absolute one.
func attachShareHosts(dataDir, projectDir string, cfg *config.Compose, st *flakegen.Stack) error {
	absProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}
	for name, svcCfg := range cfg.Services {
		svc, ok := st.Services[name]
		if !ok {
			continue
		}
		for i := range svcCfg.Volumes {
			vol := &svcCfg.Volumes[i]
			if vol.Type != "share" {
				continue
			}
			var path string
			switch {
			case vol.Host == "":
				path = filepath.Join(dataDir, "volumes", vol.Name)
				if err := os.MkdirAll(path, 0o755); err != nil {
					return fmt.Errorf("service %q: share %q: create default volume dir: %w", name, vol.Name, err)
				}
			case !filepath.IsAbs(vol.Host):
				path = filepath.Join(absProjectDir, vol.Host)
			default:
				path = vol.Host
			}
			vol.Host = path
			if svc.ShareHosts == nil {
				svc.ShareHosts = map[string]string{}
			}
			svc.ShareHosts[vol.Name] = path
		}
		cfg.Services[name] = svcCfg
		st.Services[name] = svc
	}
	return nil
}

// shareEnvKey normalizes a share tag to the env var suffix (e.g. "db-data" → "DB_DATA").
func shareEnvKey(tag string) string {
	return strings.ToUpper(strings.ReplaceAll(tag, "-", "_"))
}

// virtiofsdEnv builds the environment for a virtiofsd child process: the
// parent's full env plus MICROBE_SHARE_<TAG>, MICROBE_HOST_UID_<TAG>, and
// MICROBE_HOST_GID_<TAG> for each share/owner, and MICROBE_SHARE_NIX_STORE
// for finix services. The shell scripts baked into the nix runner reference
// these at runtime instead of baking host paths into the derivation hash.
func virtiofsdEnv(svc flakegen.Service) []string {
	env := os.Environ()
	for tag, hostPath := range svc.ShareHosts {
		env = append(env, "MICROBE_SHARE_"+shareEnvKey(tag)+"="+hostPath)
	}
	for tag, owner := range svc.ShareOwners {
		pfx := shareEnvKey(tag)
		env = append(env, "MICROBE_HOST_UID_"+pfx+"="+strconv.Itoa(owner.HostUID))
		env = append(env, "MICROBE_HOST_GID_"+pfx+"="+strconv.Itoa(owner.HostGID))
	}
	if svc.OS == "finix" {
		env = append(env, "MICROBE_SHARE_NIX_STORE=/nix/store")
	}
	return env
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
	projectDir := filepath.Dir(opts.file)
	dataDir := datadir.Dir(cfg.Name)
	if err := attachVolumeImages(dataDir, cfg, st); err != nil {
		return err
	}
	if err := attachShareHosts(dataDir, projectDir, cfg, st); err != nil {
		return err
	}
	if err := attachShareOwners(cfg, st); err != nil {
		return err
	}

	if err := flakegen.WriteStack(projectDir, st); err != nil {
		return err
	}
	interactive := !opts.dryRun && isTerminal(opts.out)
	p := newProgress(opts.out, interactive, terminalWidth(opts.out))
	p.Step("rendered %s", projectDir)

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
			outLink := filepath.Join(dataDir, "runners", svc)
			fmt.Fprintf(opts.out, "nix build %s -> %s\n", st.Services[svc].BuildTarget, outLink)
		}
	} else {
		// Each service's derivation is independent, so build them concurrently
		// instead of paying N sequential `nix build` round trips; nix's own
		// daemon serializes/parallelizes the underlying store realizations.
		//
		// A single drain goroutine serializes progress writes so concurrent
		// statusFn calls from different service goroutines don't interleave.
		type buildMsg struct{ svc, pkg string }
		statusCh := make(chan buildMsg, 16)
		var drain sync.WaitGroup
		drain.Add(1)
		go func() {
			defer drain.Done()
			for msg := range statusCh {
				p.Step("building %s: %s", msg.svc, msg.pkg)
			}
		}()

		paths := make([]string, len(selected))
		var g errgroup.Group
		for i, svc := range selected {
			i, svc := i, svc
			g.Go(func() error {
				path, err := buildRunner(
					projectDir,
					st.Services[svc].BuildTarget,
					filepath.Join(dataDir, "runners", svc),
					func(pkg string) { statusCh <- buildMsg{svc, pkg} },
				)
				if err != nil {
					return err
				}
				paths[i] = path
				return nil
			})
		}
		buildErr := g.Wait()
		close(statusCh)
		drain.Wait()
		p.Done()
		if buildErr != nil {
			return buildErr
		}
		for i, svc := range selected {
			p.Step("%s -> %s", svc, paths[i])
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
	virtiofsdPIDs := map[string]int{}
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
			path, err := runtime.EnsureVolume(opts.runner, dataDir, vol.Name, vol.Size, fsType)
			if err != nil {
				return err
			}
			p.Step("volume %s", path)
		}

		runnerPath := filepath.Join(dataDir, "runners", svc)
		runDir := filepath.Join(dataDir, "runs", svc)

		var virtiofsdPID int
		if hasVirtiofsShare(cfg.Services[svc]) {
			virtiofsdPID, err = startVirtiofsd(context.Background(), runnerPath, runDir, filepath.Join(dataDir, "logs", svc+"-virtiofsd.log"), virtiofsdEnv(st.Services[svc]))
			if err != nil {
				return err
			}
			for _, sock := range virtiofsShareSockets(svc, cfg.Services[svc]) {
				if err := waitForSocket(filepath.Join(runDir, sock), virtiofsdSocketWaitInterval, virtiofsdSocketWaitTimeout); err != nil {
					return fmt.Errorf("service %q: virtiofsd (pid %d): %w", svc, virtiofsdPID, err)
				}
			}
			virtiofsdPIDs[svc] = virtiofsdPID
			p.Step("started virtiofsd for %s (pid %d)", svc, virtiofsdPID)
		}

		pid, err := startService(context.Background(),
			runnerPath,
			runDir,
			filepath.Join(dataDir, "logs", svc+".log"))
		if err != nil {
			return err
		}
		pids[svc] = pid
		p.Step("started %s (pid %d)", svc, pid)

		if hc := cfg.Services[svc].Healthcheck; hc != nil {
			ip := st.Services[svc].IPs[primaryNetwork(st.Services[svc])]
			healthy, err := probeHealth(*hc, ip)
			if err != nil {
				return err
			}
			if healthy {
				statuses[svc] = serviceStatusHealthy
				p.Step("healthy %s", svc)
			} else {
				statuses[svc] = serviceStatusDegraded
				healthErr = fmt.Errorf("service %q did not become healthy within %s", svc, hc.StartPeriod)
				p.Done()
				fmt.Fprintf(opts.out, "degraded %s: %v\n", svc, healthErr)
				// Stop the VM we just started: leaving it running would let
				// a follow-up `up` race a second instance against it over
				// the same microvm API socket/tap.
				if err := stopService(context.Background(), pid, runtime.StopGrace); err != nil {
					fmt.Fprintf(opts.out, "warning: failed to stop degraded %s (pid %d): %v\n", svc, pid, err)
				} else {
					pids[svc] = 0
				}
				if virtiofsdPID != 0 {
					if err := stopService(context.Background(), virtiofsdPID, runtime.StopGrace); err != nil {
						fmt.Fprintf(opts.out, "warning: failed to stop virtiofsd for degraded %s (pid %d): %v\n", svc, virtiofsdPID, err)
					} else {
						virtiofsdPIDs[svc] = 0
					}
				}
				break
			}
		}
	}

	if !opts.dryRun {
		p.Done()
		store := buildStore(cfg, st, pids, virtiofsdPIDs, statuses, filepath.Join(dataDir, "runners"))
		prev, err := state.Load(filepath.Join(dataDir, "state.json"))
		if err != nil {
			return err
		}
		if opts.noProvision {
			store.Provisioned = prev.Provisioned
		} else {
			// Accumulate: a partial `up db` only provisions db's taps, but a
			// later full down must still be able to sweep services a prior
			// full up provisioned.
			store.Provisioned = dedupeNames(append(prev.Provisioned, provisionedDeviceNames(cfg, st, selected)...))
		}
		if err := store.Save(filepath.Join(dataDir, "state.json")); err != nil {
			return err
		}
		printStore(opts.out, store, isTerminal(opts.out))
	}
	if healthErr != nil {
		return healthErr
	}
	return nil
}
