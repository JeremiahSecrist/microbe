package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/runtime"
	"microbe/internal/state"
)

func newRmCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm [services...]",
		Short: "Remove disks and state for services",
		RunE: func(cmd *cobra.Command, args []string) error {
			return rmRun(args, rmOptions{
				file:  file,
				force: force,
				stdin: os.Stdin,
				out:   os.Stdout,
			})
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "do not prompt for confirmation")
	return cmd
}

type rmOptions struct {
	file  string
	force bool
	stdin io.Reader
	out   io.Writer
}

func rmRun(args []string, opts rmOptions) error {
	cfg, err := config.Load(opts.file)
	if err != nil {
		return err
	}
	base := datadir.Dir(cfg.Name)

	store, err := state.Load(filepath.Join(base, "state.json"))
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
	if len(selected) == 0 {
		fmt.Fprintln(opts.out, "no services to remove")
		return nil
	}

	if !opts.force {
		fmt.Fprintf(opts.out, "remove %s and their disks? [y/N] ", strings.Join(selected, ", "))
		line, err := bufio.NewReader(opts.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans != "y" && ans != "yes" {
			fmt.Fprintln(opts.out, "aborted")
			return nil
		}
	}

	for _, name := range selected {
		svc, ok := store.Services[name]
		if !ok {
			return fmt.Errorf("no service %q in state", name)
		}
		for _, vol := range svc.Volumes {
			path := runtime.VolumeImagePath(base, vol)
			fmt.Fprintf(opts.out, "removing %s\n", path)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		delete(store.Services, name)
		for _, net := range store.Networks {
			delete(net.Allocated, name)
		}
	}

	return store.Save(filepath.Join(base, "state.json"))
}
