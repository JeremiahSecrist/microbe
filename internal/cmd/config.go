package cmd

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/provisiond"
)

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the evaluated and validated config",
		Long: `Config loads and validates microbe.nix (-f/--file), then prints the
evaluated JSON config followed by the computed network plan: each service's
IPv6 address/MAC assignments and the /etc/hosts-style entries microbe would
render for the stack.`,
		Example: `  # sanity-check microbe.nix and print the resolved network plan
  microbe config

  # validate a stack defined somewhere else
  microbe -f stacks/myapp.nix config`,
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

			conn, err := provisiond.Dial(provisiond.SocketPath)
			if err != nil {
				return err
			}
			defer conn.Close()
			plan, _, err := resolvePlan(cfg, file, conn)
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
	for svcName := range plan.Addrs {
		svcNames = append(svcNames, svcName)
	}
	sort.Strings(svcNames)
	fmt.Println()
	fmt.Println("network plan:")
	fmt.Printf("  %-8s %-40s %s\n", "service", "addr", "macs")
	for _, svcName := range svcNames {
		var nets []string
		for netName := range plan.MACs[svcName] {
			nets = append(nets, netName)
		}
		sort.Strings(nets)
		macs := make([]string, 0, len(nets))
		for _, netName := range nets {
			macs = append(macs, netName+"="+plan.MACs[svcName][netName])
		}
		fmt.Printf("  %-8s %-40s %s\n", svcName, plan.Addrs[svcName], macs)
	}
	fmt.Println()
	fmt.Println("hosts:")
	for _, hostLine := range hostnet.RenderHosts(plan) {
		fmt.Printf("  %s\n", hostLine)
	}
}
