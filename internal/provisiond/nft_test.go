package provisiond

import (
	"testing"

	"microbe/internal/hostnet"

	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

func TestFingerprintRoundTrip(t *testing.T) {
	p := hostnet.PortSpec{HostPort: 8080, GuestIP: "fd00:1234:5678::2", GuestPort: 5432}
	fp := fingerprint(p)
	if got := userDataFingerprint([]byte(fp), userDataPrefix); got != fp {
		t.Fatalf("userDataFingerprint(%q) = %q, want %q", fp, got, fp)
	}
	if got := userDataFingerprint([]byte("unrelated"), userDataPrefix); got != "" {
		t.Fatalf("userDataFingerprint(unrelated) = %q, want empty", got)
	}
}

func TestFingerprintUnique(t *testing.T) {
	a := fingerprint(hostnet.PortSpec{HostPort: 8080, GuestIP: "fd00:1234:5678::2", GuestPort: 5432})
	b := fingerprint(hostnet.PortSpec{HostPort: 8080, GuestIP: "fd00:1234:5678::3", GuestPort: 5432})
	if a == b {
		t.Fatalf("fingerprints collide: %q", a)
	}
}

func TestDnatExprsShape(t *testing.T) {
	exprs, err := dnatExprs(hostnet.PortSpec{HostPort: 8080, GuestIP: "fd00:1234:5678::2", GuestPort: 5432})
	if err != nil {
		t.Fatal(err)
	}
	if len(exprs) != 7 {
		t.Fatalf("dnatExprs = %d expressions, want 7", len(exprs))
	}
	meta, ok := exprs[0].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyL4PROTO {
		t.Errorf("expr[0] = %#v, want l4proto meta load", exprs[0])
	}
	cmpProto, ok := exprs[1].(*expr.Cmp)
	if !ok || cmpProto.Op != expr.CmpOpEq || len(cmpProto.Data) != 1 || cmpProto.Data[0] != unix.IPPROTO_TCP {
		t.Errorf("expr[1] = %#v, want tcp proto cmp", exprs[1])
	}
	payload, ok := exprs[2].(*expr.Payload)
	if !ok || payload.Base != expr.PayloadBaseTransportHeader || payload.Offset != 2 || payload.Len != 2 {
		t.Errorf("expr[2] = %#v, want tcp dport payload load", exprs[2])
	}
	cmpPort, ok := exprs[3].(*expr.Cmp)
	if !ok || len(cmpPort.Data) != 2 || cmpPort.Data[0] != 0x1f || cmpPort.Data[1] != 0x90 {
		t.Errorf("expr[3] = %#v, want host port 8080 cmp", exprs[3])
	}
	addr, ok := exprs[4].(*expr.Immediate)
	if !ok || len(addr.Data) != 16 {
		t.Errorf("expr[4] = %#v, want 16-byte guest IPv6 immediate", exprs[4])
	}
	port, ok := exprs[5].(*expr.Immediate)
	if !ok || len(port.Data) != 2 || port.Data[0] != 0x15 || port.Data[1] != 0x38 {
		t.Errorf("expr[5] = %#v, want guest port 5432 immediate", exprs[5])
	}
	nat, ok := exprs[6].(*expr.NAT)
	if !ok || nat.Type != expr.NATTypeDestNAT || nat.Family != unix.NFPROTO_IPV6 ||
		nat.RegAddrMin != 1 || nat.RegProtoMin != 2 {
		t.Errorf("expr[6] = %#v, want dnat nat expr", exprs[6])
	}
}

func TestDnatExprsRejectsIPv4(t *testing.T) {
	if _, err := dnatExprs(hostnet.PortSpec{HostPort: 8080, GuestIP: "192.168.1.2", GuestPort: 80}); err == nil {
		t.Error("dnatExprs(ipv4 guest addr) = nil error, want error")
	}
}

func TestEstablishedAcceptExprsShape(t *testing.T) {
	exprs := establishedAcceptExprs()
	if len(exprs) != 4 {
		t.Fatalf("establishedAcceptExprs = %d expressions, want 4", len(exprs))
	}
	ct, ok := exprs[0].(*expr.Ct)
	if !ok || ct.Key != expr.CtKeySTATE {
		t.Errorf("expr[0] = %#v, want ct state load", exprs[0])
	}
	if _, ok := exprs[1].(*expr.Bitwise); !ok {
		t.Errorf("expr[1] = %#v, want bitwise mask", exprs[1])
	}
	cmp, ok := exprs[2].(*expr.Cmp)
	if !ok || cmp.Op != expr.CmpOpNeq {
		t.Errorf("expr[2] = %#v, want neq-zero cmp", exprs[2])
	}
	if _, ok := exprs[3].(*expr.Verdict); !ok {
		t.Errorf("expr[3] = %#v, want accept verdict", exprs[3])
	}
}

func TestRuleFingerprintUnique(t *testing.T) {
	a := ruleFingerprint(hostnet.RuleSpec{From: "fd00::1", To: "fd00::2", Proto: "tcp", Port: 5432})
	b := ruleFingerprint(hostnet.RuleSpec{From: "fd00::1", To: "fd00::2", Proto: "tcp", Port: 80})
	c := ruleFingerprint(hostnet.RuleSpec{From: "fd00::1", To: "fd00::3", Proto: "tcp", Port: 5432})
	if a == b || a == c || b == c {
		t.Errorf("rule fingerprints collide: a=%q b=%q c=%q", a, b, c)
	}
}

func TestRuleExprsShape(t *testing.T) {
	exprs, err := ruleExprs(hostnet.RuleSpec{From: "fd00::1", To: "fd00::2", Proto: "tcp", Port: 5432})
	if err != nil {
		t.Fatal(err)
	}
	if len(exprs) != 9 {
		t.Fatalf("ruleExprs (with port) = %d expressions, want 9", len(exprs))
	}
	saddr, ok := exprs[0].(*expr.Payload)
	if !ok || saddr.Offset != ipv6SrcOffset || saddr.Len != 16 {
		t.Errorf("expr[0] = %#v, want ip6 saddr payload load", exprs[0])
	}
	cmpFrom, ok := exprs[1].(*expr.Cmp)
	if !ok || len(cmpFrom.Data) != 16 {
		t.Errorf("expr[1] = %#v, want 16-byte from-addr cmp", exprs[1])
	}
	daddr, ok := exprs[2].(*expr.Payload)
	if !ok || daddr.Offset != ipv6DstOffset || daddr.Len != 16 {
		t.Errorf("expr[2] = %#v, want ip6 daddr payload load", exprs[2])
	}
	if _, ok := exprs[8].(*expr.Verdict); !ok {
		t.Errorf("last expr = %#v, want accept verdict", exprs[8])
	}
}

func TestRuleExprsShapeNoPort(t *testing.T) {
	exprs, err := ruleExprs(hostnet.RuleSpec{From: "fd00::1", To: "fd00::2", Proto: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	// No port -> no dport payload/cmp pair, so 2 fewer expressions than the
	// with-port case (still ends in accept).
	if len(exprs) != 7 {
		t.Fatalf("ruleExprs (no port) = %d expressions, want 7", len(exprs))
	}
	if _, ok := exprs[6].(*expr.Verdict); !ok {
		t.Errorf("last expr = %#v, want accept verdict", exprs[6])
	}
}

func TestRuleExprsRejectsIPv4(t *testing.T) {
	if _, err := ruleExprs(hostnet.RuleSpec{From: "10.0.0.1", To: "fd00::2"}); err == nil {
		t.Error("ruleExprs(ipv4 from) = nil error, want error")
	}
	if _, err := ruleExprs(hostnet.RuleSpec{From: "fd00::1", To: "10.0.0.2"}); err == nil {
		t.Error("ruleExprs(ipv4 to) = nil error, want error")
	}
}
