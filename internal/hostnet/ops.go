package hostnet

import (
	"fmt"
	"strconv"

	"microbe/internal/cmdrun"
)

// Host resource specs. The lifecycle commands (M4) build these from the
// network plan and stack, then pass them to the ops below (M3).
//
// CONTRACT (M3/M4 boundary): Agent B (lifecycle) depends on the exact type
// names, field names, and function signatures in this file. Do not rename
// them without updating Agent B.

// NetSpec is one network to provision on the host: a bridge br-<stack>-<net>
// carrying the gateway address.
type NetSpec struct {
	Name    string // network name, e.g. "backend"
	Gateway string // bridge address, e.g. "192.168.51.1"
	Prefix  int    // gateway prefix length, e.g. 24
}

// TapSpec is one tap interface, enslaved to its network's bridge.
type TapSpec struct {
	Name   string // tap id, e.g. "mvc-...-backend" (≤15 chars)
	Bridge string // bridge id, e.g. "br-<stack>-backend"
}

// PortSpec is one published port: iptables DNAT from HostPort to the guest.
type PortSpec struct {
	HostPort  int
	GuestIP   string
	GuestPort int
}

// BridgeName returns the host bridge name for a stack+network pair.
func BridgeName(stack, net string) string {
	return "br-" + stack + "-" + net
}

// EnsureNetworks creates each bridge and assigns its gateway address.
// Should be idempotent: an existing bridge/address is left in place.
func EnsureNetworks(r cmdrun.Runner, stack string, nets []NetSpec) error {
	for _, n := range nets {
		b := BridgeName(stack, n.Name)
		if err := r("ip", "link", "show", "dev", b); err != nil {
			if err := r("ip", "link", "add", b, "type", "bridge"); err != nil {
				return fmt.Errorf("hostnet: create bridge %s: %w", b, err)
			}
		}
		addr := fmt.Sprintf("%s/%d", n.Gateway, n.Prefix)
		if err := r("ip", "addr", "replace", addr, "dev", b); err != nil {
			return fmt.Errorf("hostnet: assign %s to bridge %s: %w", addr, b, err)
		}
		if err := r("ip", "link", "set", b, "up"); err != nil {
			return fmt.Errorf("hostnet: bring up bridge %s: %w", b, err)
		}
	}
	return nil
}

// EnsureTaps creates each tap and enslaves it to its bridge.
func EnsureTaps(r cmdrun.Runner, taps []TapSpec) error {
	for _, t := range taps {
		if err := r("ip", "link", "show", "dev", t.Name); err != nil {
			if err := r("ip", "tuntap", "add", "dev", t.Name, "mode", "tap"); err != nil {
				return fmt.Errorf("hostnet: create tap %s: %w", t.Name, err)
			}
		}
		if err := r("ip", "link", "set", t.Name, "master", t.Bridge); err != nil {
			return fmt.Errorf("hostnet: enslave tap %s to %s: %w", t.Name, t.Bridge, err)
		}
		if err := r("ip", "link", "set", t.Name, "up"); err != nil {
			return fmt.Errorf("hostnet: bring up tap %s: %w", t.Name, err)
		}
	}
	return nil
}

// ApplyPorts installs iptables DNAT rules for the published ports.
func ApplyPorts(r cmdrun.Runner, ports []PortSpec) error {
	for _, p := range ports {
		if err := r("iptables", dnatArgs(p, "C")...); err == nil {
			continue
		}
		if err := r("iptables", dnatArgs(p, "A")...); err != nil {
			return fmt.Errorf("hostnet: install DNAT for host port %d: %w", p.HostPort, err)
		}
	}
	return nil
}

// TeardownNetworks deletes the bridges. Best-effort: missing devices are fine.
func TeardownNetworks(r cmdrun.Runner, stack string, nets []NetSpec) error {
	for _, n := range nets {
		b := BridgeName(stack, n.Name)
		_ = r("ip", "link", "del", b)
	}
	return nil
}

// TeardownTaps deletes the taps. Best-effort.
func TeardownTaps(r cmdrun.Runner, taps []TapSpec) error {
	for _, t := range taps {
		_ = r("ip", "link", "del", t.Name)
	}
	return nil
}

// TeardownPorts removes the iptables DNAT rules. Best-effort.
func TeardownPorts(r cmdrun.Runner, ports []PortSpec) error {
	for _, p := range ports {
		_ = r("iptables", dnatArgs(p, "D")...)
	}
	return nil
}

func dnatArgs(p PortSpec, action string) []string {
	return []string{
		"-t", "nat", "-" + action, "PREROUTING",
		"-p", "tcp", "--dport", strconv.Itoa(p.HostPort),
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", p.GuestIP, p.GuestPort),
	}
}
