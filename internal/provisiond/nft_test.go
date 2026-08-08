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
	if got := userDataFingerprint([]byte(fp), userDataPrefix); got != fp {
		t.Fatalf("userDataFingerprint(%q) = %q, want %q", fp, got, fp)
	}
	if got := userDataFingerprint([]byte("unrelated"), userDataPrefix); got != "" {
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
	exprs := dnatExprs(hostnet.PortSpec{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 5432})
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
	if !ok || len(addr.Data) != 4 || addr.Data[0] != 192 {
		t.Errorf("expr[4] = %#v, want guest IP immediate", exprs[4])
	}
	port, ok := exprs[5].(*expr.Immediate)
	if !ok || len(port.Data) != 2 || port.Data[0] != 0x15 || port.Data[1] != 0x38 {
		t.Errorf("expr[5] = %#v, want guest port 5432 immediate", exprs[5])
	}
	nat, ok := exprs[6].(*expr.NAT)
	if !ok || nat.Type != expr.NATTypeDestNAT || nat.Family != unix.NFPROTO_IPV4 ||
		nat.RegAddrMin != 1 || nat.RegProtoMin != 2 {
		t.Errorf("expr[6] = %#v, want dnat nat expr", exprs[6])
	}
}

func TestSubnetCIDR(t *testing.T) {
	cases := []struct {
		gateway string
		prefix  int
		want    string
	}{
		{"192.168.51.1", 24, "192.168.51.0/24"},
		{"192.168.50.1", 24, "192.168.50.0/24"},
		{"10.0.0.5", 8, "10.0.0.0/8"},
	}
	for _, c := range cases {
		got, err := subnetCIDR(c.gateway, c.prefix)
		if err != nil {
			t.Fatalf("subnetCIDR(%q, %d): %v", c.gateway, c.prefix, err)
		}
		if got != c.want {
			t.Errorf("subnetCIDR(%q, %d) = %q, want %q", c.gateway, c.prefix, got, c.want)
		}
	}
	if _, err := subnetCIDR("not-an-ip", 24); err == nil {
		t.Error("subnetCIDR(invalid gateway) = nil error, want error")
	}
}

func TestMasqExprsShape(t *testing.T) {
	exprs, err := masqExprs("192.168.51.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if len(exprs) != 4 {
		t.Fatalf("masqExprs = %d expressions, want 4", len(exprs))
	}
	payload, ok := exprs[0].(*expr.Payload)
	if !ok || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != ipv4SrcOffset || payload.Len != 4 {
		t.Errorf("expr[0] = %#v, want ip saddr payload load", exprs[0])
	}
	bitwise, ok := exprs[1].(*expr.Bitwise)
	if !ok || len(bitwise.Mask) != 4 || bitwise.Mask[0] != 0xff || bitwise.Mask[3] != 0x00 {
		t.Errorf("expr[1] = %#v, want /24 netmask bitwise", exprs[1])
	}
	cmp, ok := exprs[2].(*expr.Cmp)
	if !ok || cmp.Op != expr.CmpOpEq || len(cmp.Data) != 4 || cmp.Data[0] != 192 || cmp.Data[3] != 0 {
		t.Errorf("expr[2] = %#v, want network address cmp", exprs[2])
	}
	if _, ok := exprs[3].(*expr.Masq); !ok {
		t.Errorf("expr[3] = %#v, want masquerade", exprs[3])
	}
	if _, err := masqExprs("not-a-cidr"); err == nil {
		t.Error("masqExprs(invalid cidr) = nil error, want error")
	}
}

func TestForwardAcceptExprsShape(t *testing.T) {
	src, err := forwardAcceptSrcExprs("192.168.51.0/24")
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := src[0].(*expr.Payload)
	if !ok || payload.Offset != ipv4SrcOffset {
		t.Errorf("forwardAcceptSrcExprs[0] = %#v, want saddr offset %d", src[0], ipv4SrcOffset)
	}
	if _, ok := src[len(src)-1].(*expr.Verdict); !ok {
		t.Errorf("forwardAcceptSrcExprs last = %#v, want accept verdict", src[len(src)-1])
	}

	dst, err := forwardAcceptDstExprs("192.168.51.0/24")
	if err != nil {
		t.Fatal(err)
	}
	payload, ok = dst[0].(*expr.Payload)
	if !ok || payload.Offset != ipv4DstOffset {
		t.Errorf("forwardAcceptDstExprs[0] = %#v, want daddr offset %d", dst[0], ipv4DstOffset)
	}
	if _, ok := dst[len(dst)-1].(*expr.Verdict); !ok {
		t.Errorf("forwardAcceptDstExprs last = %#v, want accept verdict", dst[len(dst)-1])
	}
}

func TestMasqEligibleSkipsInternal(t *testing.T) {
	nets := []hostnet.NetSpec{
		{Name: "backend", Gateway: "192.168.51.1", Prefix: 24},
		{Name: "airgap", Gateway: "192.168.52.1", Prefix: 24, Internal: true},
		{Name: "frontend", Gateway: "192.168.50.1", Prefix: 24},
	}
	got := masqEligible(nets)
	if len(got) != 2 {
		t.Fatalf("masqEligible = %d nets, want 2 (internal excluded)", len(got))
	}
	for _, n := range got {
		if n.Name == "airgap" {
			t.Errorf("masqEligible included internal network %q", n.Name)
		}
	}
}

func TestForwardDirsForInternal(t *testing.T) {
	dirs := forwardDirsFor(true)
	if len(dirs) != 1 || dirs[0].suffix != ":dst" {
		t.Errorf("forwardDirsFor(internal) = %v, want only [:dst]", dirsSuffixes(dirs))
	}
}

func TestForwardDirsForNotInternal(t *testing.T) {
	dirs := forwardDirsFor(false)
	if len(dirs) != 2 {
		t.Fatalf("forwardDirsFor(not internal) = %d dirs, want 2", len(dirs))
	}
	suffixes := dirsSuffixes(dirs)
	if suffixes[0] != ":src" || suffixes[1] != ":dst" {
		t.Errorf("forwardDirsFor(not internal) = %v, want [:src :dst]", suffixes)
	}
}

func dirsSuffixes(dirs []forwardDir) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = d.suffix
	}
	return out
}

func TestMasqFingerprintRoundTrip(t *testing.T) {
	fp := masqUserDataPrefix + "192.168.51.0/24"
	if got := userDataFingerprint([]byte(fp), masqUserDataPrefix); got != fp {
		t.Fatalf("userDataFingerprint(%q, masq) = %q, want %q", fp, got, fp)
	}
	// A DNAT fingerprint must not be picked up under the masquerade prefix.
	dnatFp := fingerprint(hostnet.PortSpec{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 80})
	if got := userDataFingerprint([]byte(dnatFp), masqUserDataPrefix); got != "" {
		t.Fatalf("userDataFingerprint(%q, masq) = %q, want empty (cross-prefix collision)", dnatFp, got)
	}
}
