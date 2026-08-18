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
//     Host-local access (e.g. curl [::1]:8080) is deliberately NOT DNAT'd
//     here.  It is served by the userspace port forwarder spawned on `up`
//     (see internal/cmd/up.go and internal/portproxy), which dials the guest
//     directly and sidesteps the kernel martian-drop: a ::1 source can't
//     carry a reply back across the bridge, so an OUTPUT-chain DNAT + ::1
//     masquerade leaves the loopback return path dead.  Keeping host-local
//     ports entirely in userspace avoids that entirely, while prerouting
//     DNAT still serves real external clients whose sources are routable.
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
	nftTable        = "microbe"
	nftChain        = "prerouting"
	nftForwardChain = "forward"
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
)

// ApplyPorts installs a prerouting DNAT rule per published port (external
// clients), plus a forward-chain accept rule for each port (so the DNAT'd
// packet passes the default-deny forward policy). Host-local access is not
// DNAT'd -- it's served by the userspace port forwarder (internal/portproxy)
// that dials the guest directly. Idempotent.
func (NetOps) ApplyPorts(ports []hostnet.PortSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, prerouting, err := ensureNatChain(c)
	if err != nil {
		return err
	}

	preExisting, err := c.GetRules(table, prerouting)
	if err != nil {
		return fmt.Errorf("provisiond: list prerouting DNAT rules: %w", err)
	}

	// Best-effort migration: a deployment that ran the old (pre-forwarder)
	// rules installed an output-chain DNAT mirror plus a ::1 postrouting
	// masquerade, both of which shadow the userspace forwarder's listener on
	// the same [::1]:<hostPort> and resurrect the martian-drop that this
	// file no longer services. Prune them so a stack provisioned by the old
	// binary self-heals on the next `up`.
	if err := pruneLegacyHostLocalNAT(c, table); err != nil {
		return err
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

// TeardownPorts removes the prerouting DNAT rules and the forward-chain
// accept rules for the published ports. Best-effort.
func (NetOps) TeardownPorts(ports []hostnet.PortSpec) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("provisiond: nftables: %w", err)
	}
	table, prerouting, err := ensureNatChain(c)
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

// ensureNatChain creates the microbe IPv6 table and prerouting nat chain if
// absent. Only external DNAT lives here: host-local ports are served by the
// userspace port forwarder.
func ensureNatChain(c *nftables.Conn) (*nftables.Table, *nftables.Chain, error) {
	table := c.AddTable(&nftables.Table{Name: nftTable, Family: nftables.TableFamilyIPv6})
	prerouting := c.AddChain(&nftables.Chain{
		Name:     nftChain,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityNATDest,
	})
	if err := c.Flush(); err != nil {
		return nil, nil, fmt.Errorf("provisiond: flush nat table setup: %w", err)
	}
	return table, prerouting, nil
}

// pruneLegacyHostLocalNAT removes rules left behind by the old host-local
// port-access strategy: the output-chain DNAT mirror (any microbe: rule in
// the "output" chain) and the postrouting ::1 masquerade. Both are fixed,
// non-per-port rules, so they're deleted wholesale. Best-effort: an absent
// chain is treated as already clean.
func pruneLegacyHostLocalNAT(c *nftables.Conn, table *nftables.Table) error {
	if chain, err := c.ListChain(table, "output"); err == nil && chain != nil {
		rules, err := c.GetRules(table, chain)
		if err != nil {
			return fmt.Errorf("provisiond: list legacy output rules: %w", err)
		}
		for _, rule := range rules {
			if userDataFingerprint(rule.UserData, userDataPrefix) == "" {
				continue
			}
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete legacy output DNAT rule: %w", err)
			}
		}
	}
	if chain, err := c.ListChain(table, "postrouting"); err == nil && chain != nil {
		rules, err := c.GetRules(table, chain)
		if err != nil {
			return fmt.Errorf("provisiond: list legacy postrouting rules: %w", err)
		}
		for _, rule := range rules {
			if string(rule.UserData) != "microbe-masq-loopback" {
				continue
			}
			if err := c.DelRule(rule); err != nil {
				return fmt.Errorf("provisiond: delete legacy postrouting masquerade: %w", err)
			}
		}
	}
	return c.Flush()
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
