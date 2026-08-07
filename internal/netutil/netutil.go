package netutil

import "net/netip"

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

func Broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	bits := p.Bits()
	for i := 3; i >= 0; i-- {
		hostBits := (i+1)*8 - bits
		if hostBits <= 0 {
			break
		}
		if hostBits > 8 {
			hostBits = 8
		}
		var mask uint8
		for k := 0; k < hostBits; k++ {
			mask |= 1 << k
		}
		a[i] |= mask
	}
	return netip.AddrFrom4(a)
}
