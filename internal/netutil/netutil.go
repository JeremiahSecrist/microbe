package netutil

import (
	"encoding/binary"
	"net/netip"
)

// ipv4Bits is the width in bits of an IPv4 address, used to derive the
// number of host bits left outside a given prefix length.
const ipv4Bits = 32

// Gateway returns the conventional gateway address for prefix: the network
// address with the last octet set to 1.
func Gateway(prefix netip.Prefix) netip.Addr {
	octets := prefix.Masked().Addr().As4()
	octets[3]++
	return netip.AddrFrom4(octets)
}

// GatewayString parses cidr and returns its conventional gateway address as
// a string.
func GatewayString(cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", err
	}
	return Gateway(prefix).String(), nil
}

// Broadcast returns the all-ones host address of prefix: the network address
// with every bit outside the prefix set to 1.
func Broadcast(prefix netip.Prefix) netip.Addr {
	octets := prefix.Masked().Addr().As4()
	network := binary.BigEndian.Uint32(octets[:])
	hostBits := ipv4Bits - prefix.Bits()
	hostMask := uint32(1)<<hostBits - 1
	binary.BigEndian.PutUint32(octets[:], network|hostMask)
	return netip.AddrFrom4(octets)
}
