package cmd

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/hostnet"
)

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the evaluated and validated config",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(file)
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			b, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(b))

			plan, err := hostnet.Plan(cfg)
			if err != nil {
				return err
			}
			printPlan(plan)
			return nil
		},
	}
}

func printPlan(p *hostnet.NetworkPlan) {
	var svcs []string
	for s := range p.IPs {
		svcs = append(svcs, s)
	}
	sort.Strings(svcs)
	fmt.Println()
	fmt.Println("network plan:")
	fmt.Printf("  %-8s %-10s %-16s %s\n", "service", "network", "ip", "mac")
	for _, s := range svcs {
		var nets []string
		for n := range p.IPs[s] {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		for _, n := range nets {
			fmt.Printf("  %-8s %-10s %-16s %s\n", s, n, p.IPs[s][n], p.MACs[s][n])
		}
	}
	fmt.Println()
	fmt.Println("hosts:")
	for _, h := range hostnet.RenderHosts(p) {
		fmt.Printf("  %s\n", h)
	}
}
