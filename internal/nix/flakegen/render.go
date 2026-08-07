package flakegen

import (
	"fmt"
	"net/netip"
)

type Networkd struct {
	MAC     string
	Address string
	Gateway string
}

func NetworkdUnit(mac, ip string, prefix int, gateway string) Networkd {
	return Networkd{
		MAC:     mac,
		Address: fmt.Sprintf("%s/%d", ip, prefix),
		Gateway: gateway,
	}
}

func Gateway(cidr string) (string, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", err
	}
	a := p.Masked().Addr().As4()
	a[3]++
	return netip.AddrFrom4(a).String(), nil
}
