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
//   - prerouting (nat): one DNAT rule per published port, straight to the
//     service's real IPv6 address -- no NAT64 needed for an IPv6-capable
//     external client to reach a published port. (A later stage adds a
//     second, v4-family DNAT chain targeting a NAT64-mapped address, for
//     IPv4-only external clients; that's additive, not a replacement.)
//   - forward (filter): default-deny (Policy: drop) plus one
//     established/related accept and one accept per config.Rule -- the
//     mechanism behind "rules: manage who can talk to who" (see
//     hostnet.RuleSpec). Replaces the old per-network-subnet
//     EnsureForwardAccept/EnsureMasquerade entirely: there's no more
//     subnet to be permissive about, and general internet egress moves to
//     NAT64 (a later stage), not nftables masquerade.
//
// Every rule (DNAT or forward-accept) carries a stable UserData fingerprint
// so an existing rule can be detected and a specific one deleted without
// comparing expression trees -- the same idempotent add/diff pattern
// throughout this file.

const (
	nftTable        = "microbe"
	nftChain        = "prerouting"
	nftForwardChain = "forward"
	nftOutputChain  = "output"
)

const (
	userDataPrefix = "microbe:"
	fwdSvcPrefix   = "microbe-fwd-svc:"
	// establishedFingerprint tags the single leading "ct state
	// established,related accept" rule so it's only ever added once.
	establishedFingerprint = "microbe-fwd-established"

	// outEstablishedFingerprint and outDnatStatusFingerprint tag the output
	// chain's two singleton rules (added once, same pattern as
	// establishedFingerprint above).
	outEstablishedFingerprint = "microbe-out-established"
	outDnatStatusFingerprint  = "microbe-out-dnat-status"
	hostAccessPrefix          = "microbe-out-hostaccess:"
	healthAccessPrefix        = "microbe-out-health:"
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
		exprs, err := dnatExprs(port)
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

// ensureNatChain creates the microbe IPv6 table and prerouting chain if
// absent. GetRules requires the chain to exist, so callers get a usable
// handle.
func ensureNatChain(c *nftables.Conn) (*nftables.Table, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv6})
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
	have := map[string]bool{}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, fwdSvcPrefix); tag != "" {
			have[tag] = true
		}
	}
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

// ---------------------------------------------------------------------
// output chain: host->guest default-deny.
//
// forward (above) governs guest<->guest/external bridged traffic; it never
// sees host-originated packets, which take the OUTPUT hook instead. Without
// a chain there, the host can reach any guest's IPv6 ULA address on any
// port directly, bypassing the "published ports only" model entirely. The
// output chain closes that: policy drop, plus established/related, plus
// "ct status dnat" (the packet was legitimately retargeted by the
// prerouting DNAT chain above -- covers both the loopback-published-port
// path and any other host-originated packet the DNAT step retargeted), plus
// one accept per opted-in HostAccessSpec/HealthAccessSpec.
// ---------------------------------------------------------------------

// ipsDstNatBit is IPS_DST_NAT_BIT from linux's nf_conntrack_common.h: the
// conntrack status bit set on a connection that went through DNAT. The
// vendored nftables Go library has no named constant for it (unlike
// CtStateBit* for CtKeySTATE), so it's hardcoded here with this citation.
const ipsDstNatBit = 1 << 5

// ensureOutputChain creates the microbe table's IPv6 output chain if absent,
// with a default-deny policy, and ensures its two singleton rules
// (established/related accept, ct-status-dnat accept) exist. Idempotent,
// mirrors ensureForwardChain.
func ensureOutputChain(c *nftables.Conn) (*nftables.Table, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv6})
	policy := nftables.ChainPolicyDrop
	chain := c.AddChain(&nftables.Chain{
		Name:     nftOutputChain,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &policy,
	})
	if err := c.Flush(); err != nil {
		return nil, nil, fmt.Errorf("provisiond: flush output chain setup: %w", err)
	}

	existing, err := c.GetRules(table, chain)
	if err != nil {
		return nil, nil, fmt.Errorf("provisiond: list output rules: %w", err)
	}
	haveEstablished, haveDnatStatus := false, false
	for _, rule := range existing {
		switch string(rule.UserData) {
		case outEstablishedFingerprint:
			haveEstablished = true
		case outDnatStatusFingerprint:
			haveDnatStatus = true
		}
	}
	if !haveEstablished {
		c.AddRule(&nftables.Rule{
			Table:    table,
			Chain:    chain,
			UserData: []byte(outEstablishedFingerprint),
			Exprs:    establishedAcceptExprs(),
		})
	}
	if !haveDnatStatus {
		c.AddRule(&nftables.Rule{
			Table:    table,
			Chain:    chain,
			UserData: []byte(outDnatStatusFingerprint),
			Exprs:    outDnatStatusAcceptExprs(),
		})
	}
	if !haveEstablished || !haveDnatStatus {
		if err := c.Flush(); err != nil {
			return nil, nil, fmt.Errorf("provisiond: flush output singleton rules: %w", err)
		}
	}
	return table, chain, nil
}

// outDnatStatusAcceptExprs builds "ct status dnat accept":
//
//	ct load status => reg 1
//	bitwise reg 1 &= IPS_DST_NAT_BIT
//	cmp neq reg 1 0
//	accept
func outDnatStatusAcceptExprs() []expr.Any {
	mask := binaryutil.NativeEndian.PutUint32(ipsDstNatBit)
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATUS},
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

// hostAccessExprs builds the expression list for one HostAccessSpec's
// accept rule: daddr-only match (no saddr -- the host has many possible
// source addresses: loopback, the bridge gateway, etc.), all ports.
//
//	payload load 16b @ network header + 24 => reg 1   (ip6 daddr)
//	cmp eq reg 1 <guestIP>
//	accept
func hostAccessExprs(s hostnet.HostAccessSpec) ([]expr.Any, error) {
	addr := net.ParseIP(s.GuestIP)
	if addr == nil || addr.To4() != nil {
		return nil, fmt.Errorf("provisiond: invalid IPv6 host-access guest address %q", s.GuestIP)
	}
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: ipv6DstOffset, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr.To16()},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}, nil
}

// hostAccessFingerprint is the stable UserData tag for one HostAccessSpec's
// accept rule.
func hostAccessFingerprint(s hostnet.HostAccessSpec) string {
	return hostAccessPrefix + s.GuestIP
}

// healthAccessExprs builds the expression list for one HealthAccessSpec's
// accept rule: daddr + dport match.
//
//	payload load 16b @ network header + 24 => reg 1   (ip6 daddr)
//	cmp eq reg 1 <guestIP>
//	payload load 2b @ transport header + 2 => reg 2   (tcp dport)
//	cmp eq reg 2 <port>
//	accept
func healthAccessExprs(s hostnet.HealthAccessSpec) ([]expr.Any, error) {
	addr := net.ParseIP(s.GuestIP)
	if addr == nil || addr.To4() != nil {
		return nil, fmt.Errorf("provisiond: invalid IPv6 health-access guest address %q", s.GuestIP)
	}
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: ipv6DstOffset, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr.To16()},
		&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 2, Data: binaryutil.BigEndian.PutUint16(uint16(s.Port))},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}, nil
}

// healthAccessFingerprint is the stable UserData tag for one
// HealthAccessSpec's accept rule.
func healthAccessFingerprint(s hostnet.HealthAccessSpec) string {
	return fmt.Sprintf("%s%s:%d", healthAccessPrefix, s.GuestIP, s.Port)
}

// ApplyHostAccess installs the output chain (see ensureOutputChain) and one
// accept rule per HostAccessSpec. Idempotent, mirrors ApplyRules.
func (NetOps) ApplyHostAccess(specs []hostnet.HostAccessSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureOutputChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list output rules: %w", err)
	}
	have := map[string]bool{}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, hostAccessPrefix); tag != "" {
			have[tag] = true
		}
	}
	for _, s := range specs {
		fp := hostAccessFingerprint(s)
		if have[fp] {
			continue
		}
		exprs, err := hostAccessExprs(s)
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

// TeardownHostAccess removes the accept rules for the given HostAccessSpecs.
// Best-effort, mirrors TeardownRules.
func (NetOps) TeardownHostAccess(specs []hostnet.HostAccessSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureOutputChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list output rules: %w", err)
	}
	want := map[string]bool{}
	for _, s := range specs {
		want[hostAccessFingerprint(s)] = true
	}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, hostAccessPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete host-access rule %s: %w", tag, err)
			}
		}
	}
	return c.Flush()
}

// ApplyHealthAccess installs the output chain (see ensureOutputChain) and
// one accept rule per HealthAccessSpec. Idempotent, mirrors ApplyRules.
func (NetOps) ApplyHealthAccess(specs []hostnet.HealthAccessSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureOutputChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list output rules: %w", err)
	}
	have := map[string]bool{}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, healthAccessPrefix); tag != "" {
			have[tag] = true
		}
	}
	for _, s := range specs {
		fp := healthAccessFingerprint(s)
		if have[fp] {
			continue
		}
		exprs, err := healthAccessExprs(s)
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

// TeardownHealthAccess removes the accept rules for the given
// HealthAccessSpecs. Best-effort, mirrors TeardownRules.
func (NetOps) TeardownHealthAccess(specs []hostnet.HealthAccessSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, chain, err := ensureOutputChain(c)
	if err != nil {
		return err
	}
	existing, err := c.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("provisiond: list output rules: %w", err)
	}
	want := map[string]bool{}
	for _, s := range specs {
		want[healthAccessFingerprint(s)] = true
	}
	for _, rule := range existing {
		if tag := userDataFingerprint(rule.UserData, healthAccessPrefix); want[tag] {
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete health-access rule %s: %w", tag, err)
			}
		}
	}
	return c.Flush()
}
