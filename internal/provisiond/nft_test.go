package provisiond

import (
	"testing"

	"microbe/internal/hostnet"

	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

func TestFingerprintRoundTrip(t *testing.T) {
	p := hostnet.PortSpec{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 5432}
	fp := fingerprint(p)
	if got := userDataFingerprint([]byte(fp)); got != fp {
		t.Fatalf("userDataFingerprint(%q) = %q, want %q", fp, got, fp)
	}
	if got := userDataFingerprint([]byte("unrelated")); got != "" {
		t.Fatalf("userDataFingerprint(unrelated) = %q, want empty", got)
	}
}

func TestFingerprintUnique(t *testing.T) {
	a := fingerprint(hostnet.PortSpec{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 5432})
	b := fingerprint(hostnet.PortSpec{HostPort: 8080, GuestIP: "192.168.51.3", GuestPort: 5432})
	if a == b {
		t.Fatalf("fingerprints collide: %q", a)
	}
}

func TestDnatExprsShape(t *testing.T) {
	es := dnatExprs(hostnet.PortSpec{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 5432})
	if len(es) != 7 {
		t.Fatalf("dnatExprs = %d expressions, want 7", len(es))
	}
	meta, ok := es[0].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyL4PROTO {
		t.Errorf("expr[0] = %#v, want l4proto meta load", es[0])
	}
	cmpProto, ok := es[1].(*expr.Cmp)
	if !ok || cmpProto.Op != expr.CmpOpEq || len(cmpProto.Data) != 1 || cmpProto.Data[0] != unix.IPPROTO_TCP {
		t.Errorf("expr[1] = %#v, want tcp proto cmp", es[1])
	}
	payload, ok := es[2].(*expr.Payload)
	if !ok || payload.Base != expr.PayloadBaseTransportHeader || payload.Offset != 2 || payload.Len != 2 {
		t.Errorf("expr[2] = %#v, want tcp dport payload load", es[2])
	}
	cmpPort, ok := es[3].(*expr.Cmp)
	if !ok || len(cmpPort.Data) != 2 || cmpPort.Data[0] != 0x1f || cmpPort.Data[1] != 0x90 {
		t.Errorf("expr[3] = %#v, want host port 8080 cmp", es[3])
	}
	addr, ok := es[4].(*expr.Immediate)
	if !ok || len(addr.Data) != 4 || addr.Data[0] != 192 {
		t.Errorf("expr[4] = %#v, want guest IP immediate", es[4])
	}
	port, ok := es[5].(*expr.Immediate)
	if !ok || len(port.Data) != 2 || port.Data[0] != 0x15 || port.Data[1] != 0x38 {
		t.Errorf("expr[5] = %#v, want guest port 5432 immediate", es[5])
	}
	nat, ok := es[6].(*expr.NAT)
	if !ok || nat.Type != expr.NATTypeDestNAT || nat.Family != unix.NFPROTO_IPV4 ||
		nat.RegAddrMin != 1 || nat.RegProtoMin != 2 {
		t.Errorf("expr[6] = %#v, want dnat nat expr", es[6])
	}
}
