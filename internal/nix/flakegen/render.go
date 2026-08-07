package flakegen

import "fmt"

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
