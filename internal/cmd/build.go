package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/nix"
	"microbe/internal/nix/flakegen"
	"microbe/internal/provisiond"
)

func newBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build [services...]",
		Short: "Render the project's flake and build runner derivations",
		Long: `Build renders flake.nix/modules/*.nix from microbe.nix (-f/--file) and
builds each selected service's runner derivation, without provisioning
networking or starting anything. With no service names given, every service
in the stack is built. Useful to pre-warm the nix store or check a stack
evaluates before running up.`,
		Example: `  # pre-build every service's runner before bringing the stack up
  microbe build

  # just rebuild one service
  microbe build web

  # see what would be built, without touching the nix store
  microbe --dry-run build`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(file)
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			conn, err := provisiond.Dial(provisiond.SocketPath)
			if err != nil {
				return err
			}
			defer conn.Close()
			plan, prefix, err := resolvePlan(cfg, file, conn)
			if err != nil {
				return err
			}
			st, err := flakegen.FromConfig(cfg, plan, prefix)
			if err != nil {
				return err
			}

			dir := filepath.Dir(file)
			dataDir := datadir.Dir(cfg.Name)
			if err := flakegen.WriteStack(dir, st); err != nil {
				return err
			}
			fmt.Printf("rendered %s\n", dir)

			services := args
			if len(services) == 0 {
				services = st.Names()
			}
			for _, svc := range services {
				if _, ok := st.Services[svc]; !ok {
					return fmt.Errorf("no service %q", svc)
				}
				outLink := filepath.Join(dataDir, "runners", svc)
				if dryRun {
					fmt.Printf("nix build .#nixosConfigurations.%s.config.microvm.declaredRunner -> %s\n", svc, outLink)
					continue
				}
				path, err := nix.BuildRunner(dir, svc, outLink, nil)
				if err != nil {
					return err
				}
				fmt.Printf("%s -> %s\n", svc, path)
			}
			return nil
		},
	}
}
