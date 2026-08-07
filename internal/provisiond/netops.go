package provisiond

import (
	"errors"
	"fmt"

	"microbe/internal/hostnet"

	"github.com/vishvananda/netlink"
)

// netops.go implements the Ops interface with netlink. It runs inside the root
// daemon (microbe-provisiond) and never shells out to ip/iptables.

// NetOps is the netlink-backed Ops implementation.
type NetOps struct{}

var _ Ops = NetOps{}

// EnsureNetworks creates each bridge and assigns its gateway address.
// Idempotent: an existing bridge/address is left in place.
func (NetOps) EnsureNetworks(stack string, nets []hostnet.NetSpec) error {
	for _, n := range nets {
		b := hostnet.BridgeName(stack, n.Name)
		link, err := netlink.LinkByName(b)
		if err != nil {
			var nf *netlink.LinkNotFoundError
			if !errors.As(err, &nf) {
				return fmt.Errorf("provisiond: look up bridge %s: %w", b, err)
			}
			link = &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: b}}
			if err := netlink.LinkAdd(link); err != nil {
				return fmt.Errorf("provisiond: create bridge %s: %w", b, err)
			}
		}
		addr, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", n.Gateway, n.Prefix))
		if err != nil {
			return fmt.Errorf("provisiond: parse %s/%d: %w", n.Gateway, n.Prefix, err)
		}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return fmt.Errorf("provisiond: assign %s/%d to bridge %s: %w", n.Gateway, n.Prefix, b, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("provisiond: bring up bridge %s: %w", b, err)
		}
	}
	return nil
}

// EnsureTaps creates each tap and enslaves it to its bridge.
func (NetOps) EnsureTaps(taps []hostnet.TapSpec) error {
	for _, t := range taps {
		link, err := netlink.LinkByName(t.Name)
		if err != nil {
			var nf *netlink.LinkNotFoundError
			if !errors.As(err, &nf) {
				return fmt.Errorf("provisiond: look up tap %s: %w", t.Name, err)
			}
			link = &netlink.Tuntap{
				LinkAttrs: netlink.LinkAttrs{Name: t.Name},
				Mode:      netlink.TUNTAP_MODE_TAP,
				NonPersist: true,
			}
			if err := netlink.LinkAdd(link); err != nil {
				return fmt.Errorf("provisiond: create tap %s: %w", t.Name, err)
			}
		}
		master, err := netlink.LinkByName(t.Bridge)
		if err != nil {
			return fmt.Errorf("provisiond: look up bridge %s for tap %s: %w", t.Bridge, t.Name, err)
		}
		if err := netlink.LinkSetMaster(link, master); err != nil {
			return fmt.Errorf("provisiond: enslave tap %s to %s: %w", t.Name, t.Bridge, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("provisiond: bring up tap %s: %w", t.Name, err)
		}
	}
	return nil
}

// TeardownNetworks deletes the bridges. Best-effort: missing devices are fine.
func (NetOps) TeardownNetworks(stack string, nets []hostnet.NetSpec) error {
	for _, n := range nets {
		_ = delLinkByName(hostnet.BridgeName(stack, n.Name))
	}
	return nil
}

// TeardownTaps deletes the taps. Best-effort.
func (NetOps) TeardownTaps(taps []hostnet.TapSpec) error {
	for _, t := range taps {
		_ = delLinkByName(t.Name)
	}
	return nil
}

func delLinkByName(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	return netlink.LinkDel(link)
}
