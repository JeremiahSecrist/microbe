package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/hostnet"
	"microbe/internal/nix/flakegen"
	"microbe/internal/provisiond"
	"microbe/internal/runtime"
	"microbe/internal/state"
)

// purge mirrors docker's prune family: `microbe purge` sweeps the current
// stack's stale network devices, while the vms/networks/volumes subcommands
// (and `--all` for host-wide scope) take down what the user explicitly asks
// for — down to "stop every VM, even ones whose origin microbe lost track
// of". Like down, every deletion is by an exact name recorded at provision
// time or reconstructed from state.json: hashed br-*/mvc-* names can't be
// attributed by prefix, so purge never guesses from the host's interface
// list (Docker's own br-* scheme would be swept in).

type purgeOptions struct {
	file   string
	dryRun bool
	all    bool
	force  bool
	ops    provisiond.Ops
	stopFn func(ctx context.Context, pid int, grace time.Duration) error
	out    io.Writer
	stdin  io.Reader
}

func newPurgeCmd() *cobra.Command {
	var all, force bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Remove stale microbe VMs, networks and volumes",
		Long: `Purge removes resources microbe no longer needs.

The bare "purge" sweeps the current stack (-f) of network devices that no
current or recorded service still uses. The subcommands go further:

  microbe purge vms          stop this stack's running VMs
  microbe purge networks     remove the stack's orphaned bridges/taps
  microbe purge volumes      delete this stack's disk images
  microbe purge all          everything, host-wide

--all (or -a) widens vm/networks/volumes across every stack under ` + "`" + datadir.Root + "`" + `,
so it can reach VMs and interfaces that no longer have a compose file behind them.`,
	}
	cmd.PersistentFlags().BoolVarP(&all, "all", "a", false, "operate on every stack under "+datadir.Root)
	cmd.PersistentFlags().BoolVarP(&force, "force", "F", false, "do not prompt for confirmation")

	mk := func(target string) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, args []string) error {
			stopFn := func(ctx context.Context, pid int, grace time.Duration) error {
				if dryRun {
					fmt.Fprintf(os.Stdout, "stop pid %d\n", pid)
					return nil
				}
				return runtime.StopService(ctx, pid, grace)
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
			return purgeRun(target, purgeOptions{
				file:   file,
				dryRun: dryRun,
				all:    all,
				force:  force,
				ops:    ops,
				stopFn: stopFn,
				out:    os.Stdout,
				stdin:  os.Stdin,
			})
		}
	}

	cmd.RunE = mk("")
	cmd.AddCommand(&cobra.Command{
		Use:   "vms",
		Short: "Stop running VMs",
		Long:  `Purge vms stops this stack's (or, with --all, every stack's) running VMs, whether or not microbe still has their pid recorded.`,
		RunE:  mk("vms"),
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "networks",
		Aliases: []string{"nets"},
		Short:   "Remove orphaned bridges/taps",
		Long:    `Purge networks removes this stack's (or, with --all, every stack's) bridge/tap devices that no current config or live VM still uses.`,
		RunE:    mk("networks"),
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "volumes",
		Aliases: []string{"vols"},
		Short:   "Delete disk images",
		Long:    `Purge volumes deletes this stack's (or, with --all, every stack's) disk images and clears the corresponding service state.`,
		RunE:    mk("volumes"),
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "all",
		Short: "Stop VMs, purge networks and volumes host-wide",
		Long:  `Purge all stops every VM, then purges networks and volumes, host-wide.`,
		RunE:  mk("all"),
	})
	return cmd
}

func purgeRun(target string, opts purgeOptions) error {
	switch target {
	case "vms":
		return purgeVMs(opts)
	case "networks":
		return purgeNetworks(opts)
	case "volumes":
		return purgeVolumes(opts)
	case "all":
		return purgeAll(opts)
	default:
		return purgeNetworks(opts)
	}
}

// loadPurgeStack loads the compose file's config, stack and persisted state.
func loadPurgeStack(file string) (*config.Compose, *flakegen.Stack, *state.Store, string, error) {
	cfg, err := config.Load(file)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, nil, "", err
	}
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		return nil, nil, nil, "", err
	}
	st, err := flakegen.FromConfig(cfg, plan)
	if err != nil {
		return nil, nil, nil, "", err
	}
	base := datadir.Dir(cfg.Name)
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		return nil, nil, nil, "", err
	}
	return cfg, st, store, base, nil
}

// loadAllStores reads every stack's store under the datadir, keyed by stack
// directory name. Stack dirs with no state.json are skipped.
func loadAllStores() (map[string]*state.Store, error) {
	entries, err := os.ReadDir(datadir.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*state.Store{}, nil
		}
		return nil, err
	}
	out := map[string]*state.Store{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := state.Load(filepath.Join(datadir.Root, e.Name(), "state.json"))
		if err != nil {
			return nil, err
		}
		if s.Stack == "" {
			continue
		}
		out[e.Name()] = s
	}
	return out, nil
}

func sortedServiceNames(store *state.Store) []string {
	names := make([]string, 0, len(store.Services))
	for name := range store.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// confirmStdin asks the user to type y/yes.
func confirmStdin(stdin io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}

// sweepOrphanedNetworkDevices deletes every recorded/reconstructible device
// name that neither the stack's current config still names nor a
// currently-running VM is attached to, then drops the swept names from the
// store so a later down/purge doesn't re-sweep ghosts.
//
// keep is derived from the stack's config when a compose file is available,
// from state alone for a host-wide purge of a stack with none, and always
// includes the devices of live VMs (state PID or answering api socket).
func sweepOrphanedNetworkDevices(opts purgeOptions, store *state.Store, stack string, cfg *config.Compose, st *flakegen.Stack) error {
	keep := map[string]bool{}
	if st != nil {
		for _, n := range currentConfigDeviceNames(cfg, st) {
			keep[n] = true
		}
	}
	base := datadir.Dir(stack)
	for _, n := range aliveDeviceNames(store, stack, base) {
		keep[n] = true
	}
	var orphanSeen bool
	candidates := dedupeNames(append(append([]string{}, store.Provisioned...), storeDeviceNames(store, stack)...))
	var orphans []string
	for _, n := range candidates {
		if !keep[n] {
			orphans = append(orphans, n)
			if !orphanSeen {
				fmt.Fprintf(opts.out, "purging %s networking\n", stack)
				orphanSeen = true
			}
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	if err := sweepOrphanLinks(opts.ops, orphans); err != nil {
		return err
	}
	for _, n := range orphans {
		fmt.Fprintf(opts.out, "purged orphaned link %s\n", n)
	}
	if opts.dryRun {
		return nil
	}
	var keepProvisioned []string
	for _, n := range store.Provisioned {
		if keep[n] {
			keepProvisioned = append(keepProvisioned, n)
		}
	}
	store.Provisioned = keepProvisioned
	return store.Save(filepath.Join(datadir.Root, stack, "state.json"))
}

func purgeNetworks(opts purgeOptions) error {
	if opts.all {
		return purgeNetworksHostWide(opts)
	}
	_, st, store, _, err := loadPurgeStack(opts.file)
	if err != nil {
		return err
	}
	return sweepOrphanedNetworkDevices(opts, store, st.Name, nil, st)
}

// purgeNetworksHostWide sweeps orphaned devices for every stack with state
// on the host, reconstructing per-stack device names from state alone. A
// stack lacking a compose file microbe is still reachable this way.
func purgeNetworksHostWide(opts purgeOptions) error {
	stores, err := loadAllStores()
	if err != nil {
		return err
	}
	for stack, store := range stores {
		if err := sweepOrphanedNetworkDevices(opts, store, stack, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func purgeVMs(opts purgeOptions) error {
	if opts.all {
		return purgeVMsHostWide(opts)
	}
	cfg, err := config.Load(opts.file)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	base := datadir.Dir(cfg.Name)
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		return err
	}
	return stopStackVMs(store, base, opts)
}

func purgeVMsHostWide(opts purgeOptions) error {
	stores, err := loadAllStores()
	if err != nil {
		return err
	}
	for stack, store := range stores {
		if err := stopStackVMs(store, datadir.Dir(stack), opts); err != nil {
			return err
		}
	}
	return nil
}

// stopStackVMs stops every recorded VM in the stack (and its virtiofsd
// companion), plus any VM whose state.json lost the pid but whose
// cloud-hypervisor API socket is still live — microbe finds that process by
// the socket's name in /proc. Clears each service back to stopped.
func stopStackVMs(store *state.Store, base string, opts purgeOptions) error {
	for _, name := range sortedServiceNames(store) {
		svc := store.Services[name]
		stopped := svc.PID > 0
		if svc.PID > 0 {
			fmt.Fprintf(opts.out, "stopping %s (pid %d)\n", name, svc.PID)
			if err := opts.stopFn(context.Background(), svc.PID, runtime.StopGrace); err != nil {
				return err
			}
			if svc.VirtiofsdPID > 0 {
				fmt.Fprintf(opts.out, "stopping virtiofsd for %s (pid %d)\n", name, svc.VirtiofsdPID)
				if err := opts.stopFn(context.Background(), svc.VirtiofsdPID, runtime.StopGrace); err != nil {
					return err
				}
			}
		}
		sock := vmSocketPath(base, name)
		if chState, err := vmState(sock); err == nil && chState != "" {
			for _, pid := range pidsForAPISocket(sock) {
				fmt.Fprintf(opts.out, "stopping unrecorded %s (pid %d)\n", name, pid)
				if err := opts.stopFn(context.Background(), pid, runtime.StopGrace); err != nil {
					return err
				}
				stopped = true
			}
		}
		if stopped && !opts.dryRun {
			_ = cleanRunDir(filepath.Join(base, "runs", name))
			svc.PID = 0
			svc.VirtiofsdPID = 0
			svc.Status = serviceStatusStopped
			store.Services[name] = svc
		}
	}
	if !opts.dryRun {
		return store.Save(filepath.Join(base, "state.json"))
	}
	return nil
}

// apiSocketProcMatch reports whether a process cmdline identifies a
// cloud-hypervisor VMM bound to the given api socket file (cmdline holds the
// socket path relative to the runner's CWD, e.g. "db.sock").
func apiSocketProcMatch(cmdline, sockBase string) bool {
	return strings.Contains(cmdline, "cloud-hypervisor") &&
		strings.Contains(cmdline, "--api-socket") &&
		strings.Contains(cmdline, sockBase)
}

// pidsForAPISocket returns the OS pids of processes whose command line names
// a live VMM on the given socket. Scoped to the calling user's processes:
// microbe VMs run as the invoking user, so this never reaches others' VMs.
var pidsForAPISocket = func(sockPath string) []int {
	sockBase := filepath.Base(sockPath)
	var pids []int
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", p.Name(), "cmdline"))
		if err != nil {
			continue
		}
		cmdline := strings.ReplaceAll(strings.ReplaceAll(string(cmd), string([]byte{0}), " "), "\n", " ")
		if cmdline == "" {
			continue
		}
		if apiSocketProcMatch(cmdline, sockBase) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func purgeVolumes(opts purgeOptions) error {
	if opts.all {
		return purgeVolumesHostWide(opts)
	}
	_, _, store, base, err := loadPurgeStack(opts.file)
	if err != nil {
		return err
	}
	return removeVolumesForStore(store, base, opts)
}

func purgeVolumesHostWide(opts purgeOptions) error {
	stores, err := loadAllStores()
	if err != nil {
		return err
	}
	for stack, store := range stores {
		if err := removeVolumesForStore(store, datadir.Dir(stack), opts); err != nil {
			return err
		}
	}
	return nil
}

// removeVolumesForStore deletes every disk image a stack's services declare
// and clears their service state, mirroring `rm` (a stack's volumes are
// useless once gone, so the service records go with them).
func removeVolumesForStore(store *state.Store, base string, opts purgeOptions) error {
	if len(store.Services) == 0 {
		fmt.Fprintln(opts.out, "no volumes to remove")
		return nil
	}
	if !opts.force {
		yes, err := confirmStdin(opts.stdin, opts.out, "remove all disk volumes and clear service state?")
		if err != nil {
			return err
		}
		if !yes {
			fmt.Fprintln(opts.out, "aborted")
			return nil
		}
	}
	for _, name := range sortedServiceNames(store) {
		svc := store.Services[name]
		for _, vol := range svc.Volumes {
			path := runtime.VolumeImagePath(base, vol)
			fmt.Fprintf(opts.out, "removing volume %s\n", path)
			if opts.dryRun {
				continue
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if !opts.dryRun {
			_ = removeSnapshots(filepath.Join(base, "runs", name))
		}
		delete(store.Services, name)
		for _, net := range store.Networks {
			delete(net.Allocated, name)
		}
	}
	if !opts.dryRun {
		store.Provisioned = nil
		return store.Save(filepath.Join(base, "state.json"))
	}
	return nil
}

func purgeAll(opts purgeOptions) error {
	opts.all = true
	if !opts.force {
		yes, err := confirmStdin(opts.stdin, opts.out, "purge all VMs, networks and volumes host-wide?")
		if err != nil {
			return err
		}
		if !yes {
			fmt.Fprintln(opts.out, "aborted")
			return nil
		}
	}
	if err := purgeVMs(opts); err != nil {
		return err
	}
	if err := purgeNetworks(opts); err != nil {
		return err
	}
	return purgeVolumes(opts)
}
