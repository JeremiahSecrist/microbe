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

func printPlan(plan *hostnet.NetworkPlan) {
	var svcNames []string
	for svcName := range plan.IPs {
		svcNames = append(svcNames, svcName)
	}
	sort.Strings(svcNames)
	fmt.Println()
	fmt.Println("network plan:")
	fmt.Printf("  %-8s %-10s %-16s %s\n", "service", "network", "ip", "mac")
	for _, svcName := range svcNames {
		var netNames []string
		for netName := range plan.IPs[svcName] {
			netNames = append(netNames, netName)
		}
		sort.Strings(netNames)
		for _, netName := range netNames {
			fmt.Printf("  %-8s %-10s %-16s %s\n", svcName, netName, plan.IPs[svcName][netName], plan.MACs[svcName][netName])
		}
	}
	fmt.Println()
	fmt.Println("hosts:")
	for _, hostLine := range hostnet.RenderHosts(plan) {
		fmt.Printf("  %s\n", hostLine)
	}
}
