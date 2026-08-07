package provisiond

import (
	"errors"
	"fmt"

	"microbe/internal/hostnet"

	"github.com/vishvananda/netlink"
)

// netops.go implements the Ops interface with netlink. It runs inside the root
// daemon (microbe-provisiond) and never shells out to ip/iptables.

// isLinkNotFound reports whether err is a "link does not exist" error from
// vishvananda/netlink. LinkByName returns LinkNotFoundError by VALUE (not
// pointer), so errors.As against *LinkNotFoundError misses it; match the value
// type explicitly.
func isLinkNotFound(err error) bool {
	var nf netlink.LinkNotFoundError
	return errors.As(err, &nf)
}

// NetOps is the netlink-backed Ops implementation.
type NetOps struct{}

var _ Ops = NetOps{}

// EnsureNetworks creates each bridge and assigns its gateway address.
// Idempotent: an existing bridge/address is left in place.
func (NetOps) EnsureNetworks(stack string, nets []hostnet.NetSpec) error {
	for _, netSpec := range nets {
		bridgeName := hostnet.BridgeName(stack, netSpec.Name)
		link, err := netlink.LinkByName(bridgeName)
		if err != nil {
			if !isLinkNotFound(err) {
				return fmt.Errorf("provisiond: look up bridge %s: %w", bridgeName, err)
			}
			link = &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: bridgeName}}
			if err := netlink.LinkAdd(link); err != nil {
				return fmt.Errorf("provisiond: create bridge %s: %w", bridgeName, err)
			}
		}
		addr, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", netSpec.Gateway, netSpec.Prefix))
		if err != nil {
			return fmt.Errorf("provisiond: parse %s/%d: %w", netSpec.Gateway, netSpec.Prefix, err)
		}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return fmt.Errorf("provisiond: assign %s/%d to bridge %s: %w", netSpec.Gateway, netSpec.Prefix, bridgeName, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("provisiond: bring up bridge %s: %w", bridgeName, err)
		}
	}
	return nil
}

// EnsureTaps creates each tap and enslaves it to its bridge.
func (NetOps) EnsureTaps(taps []hostnet.TapSpec) error {
	for _, spec := range taps {
		link, err := netlink.LinkByName(spec.Name)
		if err != nil {
			if !isLinkNotFound(err) {
				return fmt.Errorf("provisiond: look up tap %s: %w", spec.Name, err)
			}
			link = tapLink(spec)
			if err := netlink.LinkAdd(link); err != nil {
				return fmt.Errorf("provisiond: create tap %s: %w", spec.Name, err)
			}
		} else if tap, ok := link.(*netlink.Tuntap); !ok || tapNeedsRecreate(spec, tap) {
			if err := netlink.LinkDel(link); err != nil {
				return fmt.Errorf("provisiond: delete stale tap %s: %w", spec.Name, err)
			}
			link = tapLink(spec)
			if err := netlink.LinkAdd(link); err != nil {
				return fmt.Errorf("provisiond: recreate tap %s: %w", spec.Name, err)
			}
		}
		master, err := netlink.LinkByName(spec.Bridge)
		if err != nil {
			return fmt.Errorf("provisiond: look up bridge %s for tap %s: %w", spec.Bridge, spec.Name, err)
		}
		if err := netlink.LinkSetMaster(link, master); err != nil {
			return fmt.Errorf("provisiond: enslave tap %s to %s: %w", spec.Name, spec.Bridge, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("provisiond: bring up tap %s: %w", spec.Name, err)
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

// tapNeedsRecreate reports whether an existing tap's owner diverges from the
// spec: a mismatch means it was created by a different (often root) caller
// and must be deleted + recreated so the spec's uid can attach to it.
func tapNeedsRecreate(spec hostnet.TapSpec, existing *netlink.Tuntap) bool {
	return existing.Owner != uint32(spec.Owner) || existing.Group != uint32(spec.Group)
}

// tapLink builds a persistent tap device the VM process can reopen: root's
// LinkAdd calls TUNSETOWNER/TUNSETGROUP unconditionally, so the caller's uid
// must be set explicitly (a 0 owner pins the tap to root). Flags match
// `ip tuntap add ... mode tap user <u> vnet_hdr` (IFF_NO_PI | IFF_VNET_HDR),
// the shape cloud-hypervisor's virtio-net expects. The tap persists across
// the daemon connection so the later-launched VM can attach by name.
func tapLink(spec hostnet.TapSpec) netlink.Link {
	return &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: spec.Name},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Owner:     uint32(spec.Owner),
		Group:     uint32(spec.Group),
		Flags:     netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR,
	}
}
