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

// nft.go implements the Ops interface's port and rule methods against a
// single IPv6 nftables table, "microbe":
//
//   - prerouting (nat): one DNAT rule per published port — external clients
//     connecting to hostPort are redirected to the service's IPv6 address.
//   - output (nat): mirror of prerouting — host-local clients (e.g. curl
//     localhost:8080) also get DNAT'd to the service.  Without this chain,
//     locally-generated packets start in the output hook and never see the
//     prerouting DNAT.
//   - postrouting (nat): masquerade packets with src=::1 going to a bridge.
//     After output DNAT rewrites dst to the guest's IPv6 address, the source
//     is still ::1 (loopback).  The guest can't route a reply to ::1 across
//     the bridge — it would send the response to the bridge gateway, which
//     the kernel would drop as a martian.  Masquerading replaces ::1 with
//     the bridge's own address so the guest has a routable return path.
//     Only local-source traffic is masqueraded; external DNAT paths (where
//     src is the real client IP) are unaffected.
//   - forward (filter): default-deny (Policy: drop) plus one
//     established/related accept, one accept per config.Rule (service-to-
//     service), and one accept per published port (ip6 daddr <guest>
//     dport <guestPort> accept).  The per-port forward rule is required
//     because DNAT rewrites the destination before the forward hook sees the
//     packet — without it the initial SYN is dropped and ct state
//     established,related never fires.
//
// Every rule (DNAT or forward-accept) carries a stable UserData fingerprint
// so an existing rule can be detected and a specific one deleted without
// comparing expression trees -- the same idempotent add/diff pattern
// throughout this file.

const (
	nftTable           = "microbe"
	nftChain           = "prerouting"
	nftOutputChain     = "output"
	nftPostroutingChain = "postrouting"
	nftForwardChain    = "forward"
)

const (
	userDataPrefix = "microbe:"
	fwdSvcPrefix   = "microbe-fwd-svc:"
	// portFwdPrefix tags the per-published-port accept rules in the forward
	// chain so they can be distinguished from service-to-service rules.
	portFwdPrefix = "microbe-port:"
	// establishedFingerprint tags the single leading "ct state
	// established,related accept" rule so it's only ever added once.
	establishedFingerprint = "microbe-fwd-established"
	// masqLoopbackFingerprint tags the postrouting masquerade rule for ::1
	// source addresses (loopback NAT for host-local port access).
	masqLoopbackFingerprint = "microbe-masq-loopback"
)

// ApplyPorts installs DNAT rules in both the prerouting and output chains
// (so both external and host-local clients reach the VM), plus a
// forward-chain accept rule for each port (so the DNAT'd packet passes the
// default-deny forward policy). Idempotent.
func (NetOps) ApplyPorts(ports []hostnet.PortSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, prerouting, output, err := ensureNatChains(c)
	if err != nil {
		return err
	}

	preExisting, err := c.GetRules(table, prerouting)
	if err != nil {
		return fmt.Errorf("provisiond: list prerouting DNAT rules: %w", err)
	}
	outExisting, err := c.GetRules(table, output)
	if err != nil {
		return fmt.Errorf("provisiond: list output DNAT rules: %w", err)
	}

	fwdTable, fwdChain, err := ensureForwardChain(c)
	if err != nil {
		return err
	}
	fwdExisting, err := c.GetRules(fwdTable, fwdChain)
	if err != nil {
		return fmt.Errorf("provisiond: list forward rules: %w", err)
	}

	haveInPre := fingerprintSet(preExisting, userDataPrefix)
	haveInOut := fingerprintSet(outExisting, userDataPrefix)
	haveInFwd := fingerprintSet(fwdExisting, portFwdPrefix)

	for _, port := range ports {
		fp := fingerprint(port)
		exprs, err := dnatExprs(port)
		if err != nil {
			return err
		}
		if !haveInPre[fp] {
			c.AddRule(&nftables.Rule{Table: table, Chain: prerouting, UserData: []byte(fp), Exprs: exprs})
		}
		if !haveInOut[fp] {
			c.AddRule(&nftables.Rule{Table: table, Chain: output, UserData: []byte(fp), Exprs: exprs})
		}

		pfp := portFwdFingerprint(port)
		if !haveInFwd[pfp] {
			fwdExprs, err := portForwardAcceptExprs(port)
			if err != nil {
				return err
			}
			c.AddRule(&nftables.Rule{Table: fwdTable, Chain: fwdChain, UserData: []byte(pfp), Exprs: fwdExprs})
		}
	}
	return c.Flush()
}

// TeardownPorts removes the DNAT rules (prerouting + output) and the
// forward-chain accept rules for the published ports. Best-effort.
func (NetOps) TeardownPorts(ports []hostnet.PortSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, prerouting, output, err := ensureNatChains(c)
	if err != nil {
		return err
	}

	want := map[string]bool{}
	wantFwd := map[string]bool{}
	for _, port := range ports {
		want[fingerprint(port)] = true
		wantFwd[portFwdFingerprint(port)] = true
	}

	preExisting, err := c.GetRules(table, prerouting)
	if err != nil {
		return fmt.Errorf("provisiond: list prerouting DNAT rules: %w", err)
	}
	for _, rule := range preExisting {
		if tag := userDataFingerprint(rule.UserData, userDataPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete prerouting DNAT rule %s: %w", tag, err)
			}
		}
	}

	outExisting, err := c.GetRules(table, output)
	if err != nil {
		return fmt.Errorf("provisiond: list output DNAT rules: %w", err)
	}
	for _, rule := range outExisting {
		if tag := userDataFingerprint(rule.UserData, userDataPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete output DNAT rule %s: %w", tag, err)
			}
		}
	}

	fwdTable, fwdChain, err := ensureForwardChain(c)
	if err != nil {
		return err
	}
	fwdExisting, err := c.GetRules(fwdTable, fwdChain)
	if err != nil {
		return fmt.Errorf("provisiond: list forward rules: %w", err)
	}
	for _, rule := range fwdExisting {
		if tag := userDataFingerprint(rule.UserData, portFwdPrefix); wantFwd[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete port-forward accept rule %s: %w", tag, err)
			}
		}
	}

	return c.Flush()
}

// ensureNatChains creates the microbe IPv6 table and all three NAT chains
// (prerouting, output, postrouting) if absent. Prerouting catches external
// traffic, output catches host-local traffic, postrouting masquerades ::1
// sources so the loopback-DNAT path has a routable return address.
func ensureNatChains(c *nftables.Conn) (*nftables.Table, *nftables.Chain, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv6})
	prerouting := c.AddChain(&nftables.Chain{
		Name:     nftChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityNATDest,
	})
	output := c.AddChain(&nftables.Chain{
		Name:     nftOutputChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityNATDest,
	})
	postrouting := c.AddChain(&nftables.Chain{
		Name:     nftPostroutingChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPostrouting,
		Priority: nftables.ChainPriorityNATSource,
	})
	if err := c.Flush(); err != nil {
		return nil, nil, nil, fmt.Errorf("provisiond: flush nat table setup: %w", err)
	}

	// Ensure the single postrouting masquerade rule exists. This is a
	// structural rule (not per-port) so it lives here alongside the
	// established-accept rule in ensureForwardChain.
	existing, err := c.GetRules(table, postrouting)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("provisiond: list postrouting rules: %w", err)
	}
	for _, rule := range existing {
		if string(rule.UserData) == masqLoopbackFingerprint {
			return table, prerouting, output, nil
		}
	}
	c.AddRule(&nftables.Rule{
		Table:    table,
		Chain:    postrouting,
		UserData: []byte(masqLoopbackFingerprint),
		Exprs:    masqLoopbackExprs(),
	})
	if err := c.Flush(); err != nil {
		return nil, nil, nil, fmt.Errorf("provisiond: flush postrouting masquerade rule: %w", err)
	}
	return table, prerouting, output, nil
}

// masqLoopbackExprs builds "ip6 saddr ::1 masquerade" for the postrouting
// chain. This SNAT replaces the ::1 source on output-DNAT'd packets with
// the outgoing bridge's own address, giving the guest a routable return path.
//
//	payload load 16b @ network header + 8  => reg 1   (ip6 saddr)
//	cmp eq reg 1 ::1
//	masquerade
func masqLoopbackExprs() []expr.Any {
	loopback := net.ParseIP("::1").To16()
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: ipv6SrcOffset, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: loopback},
		&expr.Masq{},
	}
}

// dnatExprs builds the expression list for one DNAT rule:
//
//	meta load l4proto => reg 1
//	cmp eq reg 1 tcp
//	payload load 2b @ transport header + 2 => reg 1   (tcp dport)
//	cmp eq reg 1 <hostPort>
//	immediate reg 1 <guestIP (16 bytes)>
//	immediate reg 2 <guestPort>
//	nat dnat ip6 addr_min reg 1 proto_min reg 2
func dnatExprs(p hostnet.PortSpec) ([]expr.Any, error) {
	addr := net.ParseIP(p.GuestIP)
	if addr == nil || addr.To4() != nil {
		return nil, fmt.Errorf("provisiond: invalid IPv6 guest address %q", p.GuestIP)
	}
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
		&expr.Immediate{Register: 1, Data: addr.To16()},
		&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(uint16(p.GuestPort))},
		&expr.NAT{
			Type:        expr.NATTypeDestNAT,
			Family:      unix.NFPROTO_IPV6,
			RegAddrMin:  1,
			RegProtoMin: 2,
		},
	}, nil
}

// portForwardAcceptExprs builds the forward-chain accept rule for a published
// port. After prerouting (or output) DNAT rewrites the destination, the
// forward chain sees the rewritten ip6 daddr and guestPort. Without an
// explicit accept the default-deny forward policy drops the initial SYN.
//
//	payload load 16b @ network header + 24 => reg 1   (ip6 daddr)
//	cmp eq reg 1 <guestIP>
//	meta load l4proto => reg 2
//	cmp eq reg 2 tcp
//	payload load 2b @ transport header + 2 => reg 3   (tcp dport)
//	cmp eq reg 3 <guestPort>
//	accept
func portForwardAcceptExprs(p hostnet.PortSpec) ([]expr.Any, error) {
	addr := net.ParseIP(p.GuestIP)
	if addr == nil || addr.To4() != nil {
		return nil, fmt.Errorf("provisiond: invalid IPv6 guest address %q", p.GuestIP)
	}
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: ipv6DstOffset, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr.To16()},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 2, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Payload{DestRegister: 3, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 3, Data: binaryutil.BigEndian.PutUint16(uint16(p.GuestPort))},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}, nil
}

// fingerprint is the stable UserData tag for a published port's DNAT rule.
func fingerprint(p hostnet.PortSpec) string {
	return fmt.Sprintf("%s%d:%s:%d", userDataPrefix, p.HostPort, p.GuestIP, p.GuestPort)
}

// portFwdFingerprint is the stable UserData tag for a published port's
// forward-chain accept rule.
func portFwdFingerprint(p hostnet.PortSpec) string {
	return fmt.Sprintf("%s%s:%d", portFwdPrefix, p.GuestIP, p.GuestPort)
}

// userDataFingerprint extracts the fingerprint from a rule's UserData, or ""
// if the rule's tag doesn't carry the given prefix.
func userDataFingerprint(ud []byte, prefix string) string {
	if !bytes.HasPrefix(ud, []byte(prefix)) {
		return ""
	}
	return string(ud)
}

// fingerprintSet builds a set of fingerprints from a rule list, filtering by
// prefix.
func fingerprintSet(rules []*nftables.Rule, prefix string) map[string]bool {
	m := map[string]bool{}
	for _, rule := range rules {
		if tag := userDataFingerprint(rule.UserData, prefix); tag != "" {
			m[tag] = true
		}
	}
	return m
}

// ensureForwardChain creates the microbe table's IPv6 forward chain if
// absent, with a default-deny policy, and ensures the single leading
// established/related accept rule exists -- the two structural pieces every
// stack's `up` needs regardless of that stack's own rules: list. Idempotent.
func ensureForwardChain(c *nftables.Conn) (*nftables.Table, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv6})
	policy := nftables.ChainPolicyDrop
	chain := c.AddChain(&nftables.Chain{
		Name:     nftForwardChain,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &policy,
	})
	if err := c.Flush(); err != nil {
		return nil, nil, fmt.Errorf("provisiond: flush forward chain setup: %w", err)
	}

	existing, err := c.GetRules(table, chain)
	if err != nil {
		return nil, nil, fmt.Errorf("provisiond: list forward rules: %w", err)
	}
	for _, rule := range existing {
		if string(rule.UserData) == establishedFingerprint {
			return table, chain, nil
		}
	}
	c.AddRule(&nftables.Rule{
		Table:    table,
		Chain:    chain,
		UserData: []byte(establishedFingerprint),
		Exprs:    establishedAcceptExprs(),
	})
	if err := c.Flush(); err != nil {
		return nil, nil, fmt.Errorf("provisiond: flush established-accept rule: %w", err)
	}
	return table, chain, nil
}

// establishedAcceptExprs builds "ct state established,related accept":
//
//	ct load state => reg 1
//	bitwise reg 1 &= (ESTABLISHED|RELATED)
//	cmp neq reg 1 0
//	accept
func establishedAcceptExprs() []expr.Any {
	mask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           mask,
			Xor:            []byte{0, 0, 0, 0},
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// ApplyRules installs the forward chain (see ensureForwardChain) and one
// accept rule per RuleSpec -- the explicit-allow half of default-deny.
// Idempotent: a rule already carrying the matching fingerprint is left in
// place.
func (NetOps) ApplyRules(rules []hostnet.RuleSpec) error {
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
		return fmt.Errorf("provisiond: list forward rules: %w", err)
	}
	have := fingerprintSet(existing, fwdSvcPrefix)
	for _, r := range rules {
		fp := ruleFingerprint(r)
		if have[fp] {
			continue
		}
		exprs, err := ruleExprs(r)
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

// TeardownRules removes the forward-accept rules for the given RuleSpecs.
// Best-effort, mirrors TeardownPorts.
func (NetOps) TeardownRules(rules []hostnet.RuleSpec) error {
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
		return fmt.Errorf("provisiond: list forward rules: %w", err)
	}
	want := map[string]bool{}
	for _, r := range rules {
		want[ruleFingerprint(r)] = true
	}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, fwdSvcPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete forward rule %s: %w", tag, err)
			}
		}
	}
	return c.Flush()
}

// ruleFingerprint is the stable UserData tag for one RuleSpec's accept rule.
func ruleFingerprint(r hostnet.RuleSpec) string {
	return fmt.Sprintf("%s%s>%s:%s:%d", fwdSvcPrefix, r.From, r.To, r.Proto, r.Port)
}

// ruleExprs builds the expression list for one RuleSpec's accept rule:
//
//	payload load 16b @ network header + 8  => reg 1   (ip6 saddr)
//	cmp eq reg 1 <from>
//	payload load 16b @ network header + 24 => reg 2   (ip6 daddr)
//	cmp eq reg 2 <to>
//	meta load l4proto => reg 3
//	cmp eq reg 3 <proto>
//	[ payload load 2b @ transport header + 2 => reg 4   (dport)
//	  cmp eq reg 4 <port> ]                              (only if Port != 0)
//	accept
//
// No mask/bitwise is needed the way the old subnet-CIDR matching required:
// From/To are exact host addresses (a service's one /128), not subnets.
func ruleExprs(r hostnet.RuleSpec) ([]expr.Any, error) {
	from := net.ParseIP(r.From)
	to := net.ParseIP(r.To)
	if from == nil || from.To4() != nil {
		return nil, fmt.Errorf("provisiond: invalid IPv6 rule from-addr %q", r.From)
	}
	if to == nil || to.To4() != nil {
		return nil, fmt.Errorf("provisiond: invalid IPv6 rule to-addr %q", r.To)
	}
	proto := r.Proto
	if proto == "" {
		proto = "tcp"
	}
	protoNum := byte(unix.IPPROTO_TCP)
	if proto == "udp" {
		protoNum = unix.IPPROTO_UDP
	}

	exprs := []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: ipv6SrcOffset, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: from.To16()},
		&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseNetworkHeader, Offset: ipv6DstOffset, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 2, Data: to.To16()},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 3},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 3, Data: []byte{protoNum}},
	}
	if r.Port != 0 {
		exprs = append(exprs,
			&expr.Payload{DestRegister: 4, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 4, Data: binaryutil.BigEndian.PutUint16(uint16(r.Port))},
		)
	}
	exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictAccept})
	return exprs, nil
}

// ipv6SrcOffset and ipv6DstOffset are the network-header byte offsets of the
// IPv6 source/destination address (fixed 40-byte header: version/traffic
// class/flow label/payload length/next header/hop limit occupy the first 8
// bytes, then a 16-byte saddr, then a 16-byte daddr).
const (
	ipv6SrcOffset = 8
	ipv6DstOffset = 24
)
