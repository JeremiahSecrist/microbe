package netutil

import (
	"encoding/binary"
	"net/netip"
)

func Gateway(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	a[3]++
	return netip.AddrFrom4(a)
}

func GatewayString(cidr string) (string, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", err
	}
	return Gateway(p).String(), nil
}

// Broadcast returns the all-ones host address of p: the network address with
// every bit outside the prefix set to 1.
func Broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	network := binary.BigEndian.Uint32(a[:])
	hostMask := uint32(1)<<(32-p.Bits()) - 1
	binary.BigEndian.PutUint32(a[:], network|hostMask)
	return netip.AddrFrom4(a)
}
