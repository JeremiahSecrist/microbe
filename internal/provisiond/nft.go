package provisiond

import (
	"bytes"
	"fmt"
	"net"

	"microbe/internal/hostnet"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// nft.go implements the Ops interface's port methods using nftables DNAT.
//
// microbe owns a dedicated ip family table, "microbe", with a single
// prerouting nat chain. Each published port is one DNAT rule tagged with a
// stable UserData fingerprint ("microbe:<host>:<guestIP>:<guestPort>") so we
// can detect an existing rule and delete a specific one without comparing
// expression trees.

const (
	nftTable            = "microbe"
	nftChain            = "prerouting"
	nftPostroutingChain = "postrouting"
	nftForwardChain     = "forward"
)

const (
	userDataPrefix     = "microbe:"
	masqUserDataPrefix = "microbe-masq:"
	fwdUserDataPrefix  = "microbe-fwd:"
)

// ApplyPorts installs DNAT rules for the published ports. Idempotent: a rule
// already carrying the matching fingerprint is left in place.
func (NetOps) ApplyPorts(ports []hostnet.PortSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureNatChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list DNAT rules: %w", err)
	}
	have := map[string]bool{}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, userDataPrefix); tag != "" {
			have[tag] = true
		}
	}
	for _, port := range ports {
		fp := fingerprint(port)
		if have[fp] {
			continue
		}
		c.AddRule(&nftables.Rule{
			Table:    table,
			Chain:    chain,
			UserData: []byte(fp),
			Exprs:    dnatExprs(port),
		})
	}
	return c.Flush()
}

// TeardownPorts removes the DNAT rules for the published ports. Best-effort.
func (NetOps) TeardownPorts(ports []hostnet.PortSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureNatChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list DNAT rules: %w", err)
	}
	want := map[string]bool{}
	for _, port := range ports {
		want[fingerprint(port)] = true
	}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, userDataPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete DNAT rule %s: %w", tag, err)
			}
		}
	}
	return c.Flush()
}

// ensureNatChain creates the microbe nat table and prerouting chain if absent.
// GetRules requires the chain to exist, so callers get a usable handle.
func ensureNatChain(c *nftables.Conn) (*nftables.Table, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv4})
	chain := c.AddChain(&nftables.Chain{
		Name:     nftChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityNATDest,
	})
	if err := c.Flush(); err != nil {
		return nil, nil, fmt.Errorf("provisiond: flush nat table setup: %w", err)
	}
	return table, chain, nil
}

// dnatExprs builds the expression list for one DNAT rule:
//
//	meta load l4proto => reg 1
//	cmp eq reg 1 tcp
//	payload load 2b @ transport header + 2 => reg 1   (tcp dport)
//	cmp eq reg 1 <hostPort>
//	immediate reg 1 <guestIP>
//	immediate reg 2 <guestPort>
//	nat dnat ip addr_min reg 1 proto_min reg 2
func dnatExprs(p hostnet.PortSpec) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Payload{
			DestRegister: 1,
			Base:         expr.PayloadBaseTransportHeader,
			Offset:       2,
			Len:          2,
		},
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     binaryutil.BigEndian.PutUint16(uint16(p.HostPort)),
		},
		&expr.Immediate{Register: 1, Data: net.ParseIP(p.GuestIP).To4()},
		&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(uint16(p.GuestPort))},
		&expr.NAT{
			Type:        expr.NATTypeDestNAT,
			Family:      unix.NFPROTO_IPV4,
			RegAddrMin:  1,
			RegProtoMin: 2,
		},
	}
}

// fingerprint is the stable UserData tag for a published port rule.
func fingerprint(p hostnet.PortSpec) string {
	return fmt.Sprintf("%s%d:%s:%d", userDataPrefix, p.HostPort, p.GuestIP, p.GuestPort)
}

// userDataFingerprint extracts the fingerprint from a rule's UserData, or ""
// if the rule's tag doesn't carry the given prefix.
func userDataFingerprint(ud []byte, prefix string) string {
	if !bytes.HasPrefix(ud, []byte(prefix)) {
		return ""
	}
	return string(ud)
}

// masqEligible returns the subset of nets that should get a masquerade
// rule: internal networks (config.Network.Internal) are airgapped from
// outbound access by omission — they get no masquerade rule at all, so
// guest-initiated traffic leaving the bridge has no path out.
func masqEligible(nets []hostnet.NetSpec) []hostnet.NetSpec {
	var out []hostnet.NetSpec
	for _, n := range nets {
		if !n.Internal {
			out = append(out, n)
		}
	}
	return out
}

// EnsureMasquerade installs one masquerade rule per non-internal network,
// so traffic sourced from a stack's bridge subnet gets SNAT'd to whatever
// address the host's own routing picks for its egress interface — giving
// guests a path to the internet the same way a DNAT rule gives the host a
// path to a guest's published port. Idempotent, mirrors ApplyPorts.
func (NetOps) EnsureMasquerade(nets []hostnet.NetSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensurePostroutingChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list masquerade rules: %w", err)
	}
	have := map[string]bool{}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, masqUserDataPrefix); tag != "" {
			have[tag] = true
		}
	}
	for _, n := range masqEligible(nets) {
		cidr, err := subnetCIDR(n.Gateway, n.Prefix)
		if err != nil {
			return err
		}
		fp := masqUserDataPrefix + cidr
		if have[fp] {
			continue
		}
		exprs, err := masqExprs(cidr)
		if err != nil {
			return err
		}
		c.AddRule(&nftables.Rule{
			Table:    table,
			Chain:    chain,
			UserData: []byte(fp),
			Exprs:    exprs,
		})
	}
	return c.Flush()
}

// TeardownMasquerade removes the masquerade rules for the given networks.
// Best-effort, mirrors TeardownPorts.
func (NetOps) TeardownMasquerade(nets []hostnet.NetSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensurePostroutingChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list masquerade rules: %w", err)
	}
	want := map[string]bool{}
	for _, n := range nets {
		cidr, err := subnetCIDR(n.Gateway, n.Prefix)
		if err != nil {
			return err
		}
		want[masqUserDataPrefix+cidr] = true
	}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, masqUserDataPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete masquerade rule %s: %w", tag, err)
			}
		}
	}
	return c.Flush()
}

// forwardDir is one direction of forward-accept rule EnsureForwardAccept
// can install for a network.
type forwardDir struct {
	suffix string
	build  func(string) ([]expr.Any, error)
}

var (
	forwardDirSrc = forwardDir{":src", forwardAcceptSrcExprs}
	forwardDirDst = forwardDir{":dst", forwardAcceptDstExprs}
)

// forwardDirsFor returns the forward-accept directions to install for a
// network: an internal network (airgapped, see masqEligible) gets only the
// inbound ":dst" accept, so published-port DNAT can still reach it, but no
// ":src" accept, since that direction is what would let its own
// guest-initiated traffic pass the forward chain outbound.
func forwardDirsFor(internal bool) []forwardDir {
	if internal {
		return []forwardDir{forwardDirDst}
	}
	return []forwardDir{forwardDirSrc, forwardDirDst}
}

// EnsureForwardAccept installs forward-chain accept rules per network —
// matching traffic sourced from the subnet (guest -> internet) and traffic
// destined to it (the return path back to the guest, or inbound DNAT'd
// published-port traffic) — so microbe's own nftables table doesn't drop
// its guests' forwarded traffic. See forwardDirsFor for the internal-network
// exception. Defense-in-depth only: it guarantees this table doesn't block
// its own stacks, not that some other table on the host (e.g. a consuming
// flake that enables NixOS's networking.firewall.filterForward) won't.
// Idempotent, mirrors ApplyPorts.
func (NetOps) EnsureForwardAccept(nets []hostnet.NetSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureForwardChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list forward-accept rules: %w", err)
	}
	have := map[string]bool{}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, fwdUserDataPrefix); tag != "" {
			have[tag] = true
		}
	}
	for _, n := range nets {
		cidr, err := subnetCIDR(n.Gateway, n.Prefix)
		if err != nil {
			return err
		}
		for _, dir := range forwardDirsFor(n.Internal) {
			fp := fwdUserDataPrefix + cidr + dir.suffix
			if have[fp] {
				continue
			}
			exprs, err := dir.build(cidr)
			if err != nil {
				return err
			}
			c.AddRule(&nftables.Rule{
				Table:    table,
				Chain:    chain,
				UserData: []byte(fp),
				Exprs:    exprs,
			})
		}
	}
	return c.Flush()
}

// TeardownForwardAccept removes the forward-accept rules for the given
// networks. Best-effort, mirrors TeardownPorts.
func (NetOps) TeardownForwardAccept(nets []hostnet.NetSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureForwardChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list forward-accept rules: %w", err)
	}
	want := map[string]bool{}
	for _, n := range nets {
		cidr, err := subnetCIDR(n.Gateway, n.Prefix)
		if err != nil {
			return err
		}
		want[fwdUserDataPrefix+cidr+":src"] = true
		want[fwdUserDataPrefix+cidr+":dst"] = true
	}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, fwdUserDataPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete forward-accept rule %s: %w", tag, err)
			}
		}
	}
	return c.Flush()
}

// ensurePostroutingChain creates the microbe nat table's postrouting chain
// if absent, for masquerade rules.
func ensurePostroutingChain(c *nftables.Conn) (*nftables.Table, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv4})
	chain := c.AddChain(&nftables.Chain{
		Name:     nftPostroutingChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPostrouting,
		Priority: nftables.ChainPriorityNATSource,
	})
	if err := c.Flush(); err != nil {
		return nil, nil, fmt.Errorf("provisiond: flush postrouting chain setup: %w", err)
	}
	return table, chain, nil
}

// ensureForwardChain creates the microbe table's forward chain if absent,
// for the forward-accept rules.
func ensureForwardChain(c *nftables.Conn) (*nftables.Table, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv4})
	chain := c.AddChain(&nftables.Chain{
		Name:     nftForwardChain,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
	})
	if err := c.Flush(); err != nil {
		return nil, nil, fmt.Errorf("provisiond: flush forward chain setup: %w", err)
	}
	return table, chain, nil
}

// subnetCIDR masks gateway by prefix to get the network address, returning
// "<network>/<prefix>" (e.g. "192.168.51.1", 24 -> "192.168.51.0/24").
func subnetCIDR(gateway string, prefix int) (string, error) {
	ip := net.ParseIP(gateway).To4()
	if ip == nil {
		return "", fmt.Errorf("provisiond: invalid gateway %q", gateway)
	}
	network := ip.Mask(net.CIDRMask(prefix, 32))
	return fmt.Sprintf("%s/%d", network.String(), prefix), nil
}

// ipv4AddrCmpExprs builds the shared prefix of an "ip saddr/daddr <cidr>"
// match: load the network-header address at offset (12 for source, 16 for
// destination), mask it by the subnet's netmask, and compare against the
// subnet's network address.
func ipv4AddrCmpExprs(cidr string, offset uint32) ([]expr.Any, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("provisiond: parse cidr %q: %w", cidr, err)
	}
	return []expr.Any{
		&expr.Payload{
			DestRegister: 1,
			Base:         expr.PayloadBaseNetworkHeader,
			Offset:       offset,
			Len:          4,
		},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           []byte(ipnet.Mask),
			Xor:            []byte{0, 0, 0, 0},
		},
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     ipnet.IP.To4(),
		},
	}, nil
}

// ipv4SrcOffset and ipv4DstOffset are the network-header byte offsets of
// the IPv4 source/destination address.
const (
	ipv4SrcOffset = 12
	ipv4DstOffset = 16
)

// masqExprs builds the expression list for one masquerade rule:
//
//	payload load 4b @ network header + 12 => reg 1   (ip saddr)
//	bitwise reg1 &= <netmask>
//	cmp eq reg 1 <network addr>
//	masq
func masqExprs(cidr string) ([]expr.Any, error) {
	exprs, err := ipv4AddrCmpExprs(cidr, ipv4SrcOffset)
	if err != nil {
		return nil, err
	}
	return append(exprs, &expr.Masq{}), nil
}

// forwardAcceptSrcExprs builds a forward-accept rule matching "ip saddr
// <cidr>" — the guest-to-internet direction.
func forwardAcceptSrcExprs(cidr string) ([]expr.Any, error) {
	exprs, err := ipv4AddrCmpExprs(cidr, ipv4SrcOffset)
	if err != nil {
		return nil, err
	}
	return append(exprs, &expr.Verdict{Kind: expr.VerdictAccept}), nil
}

// forwardAcceptDstExprs builds a forward-accept rule matching "ip daddr
// <cidr>" — the internet-to-guest return direction.
func forwardAcceptDstExprs(cidr string) ([]expr.Any, error) {
	exprs, err := ipv4AddrCmpExprs(cidr, ipv4DstOffset)
	if err != nil {
		return nil, err
	}
	return append(exprs, &expr.Verdict{Kind: expr.VerdictAccept}), nil
}
