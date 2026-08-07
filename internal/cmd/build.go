package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/nix"
	"microbe/internal/nix/flakegen"
)

func newBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build [services...]",
		Short: "Render .microbe/ and build runner derivations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(file)
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

			dir := ".microbe"
			if err := flakegen.WriteStack(dir, st, file); err != nil {
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
				out := filepath.Join(dir, "runners", svc)
				if dryRun {
					fmt.Printf("nix build .#nixosConfigurations.%s.config.microvm.declaredRunner -> %s\n", svc, out)
					continue
				}
				path, err := nix.BuildRunner(dir, svc, out)
				if err != nil {
					return err
				}
				fmt.Printf("%s -> %s\n", svc, path)
			}
			return nil
		},
	}
}
