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
	nftTable = "microbe"
	nftChain = "prerouting"
)

const userDataPrefix = "microbe:"

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
		if tag := userDataFingerprint(rule.UserData); tag != "" {
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
		if tag := userDataFingerprint(rule.UserData); want[tag] {
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
// if the rule is not one of ours.
func userDataFingerprint(ud []byte) string {
	if !bytes.HasPrefix(ud, []byte(userDataPrefix)) {
		return ""
	}
	return string(ud)
}
