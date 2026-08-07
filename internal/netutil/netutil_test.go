package netutil

import (
	"net/netip"
	"testing"
)

func TestGateway(t *testing.T) {
	cases := map[string]string{
		"192.168.51.0/24": "192.168.51.1",
		"192.168.0.0/16":  "192.168.0.1",
		"10.10.10.0/24":   "10.10.10.1",
		"10.0.0.0/30":     "10.0.0.1",
	}
	for cidr, want := range cases {
		got, err := GatewayString(cidr)
		if err != nil {
			t.Errorf("GatewayString(%q) error: %v", cidr, err)
			continue
		}
		if got != want {
			t.Errorf("GatewayString(%q) = %q, want %q", cidr, got, want)
		}
	}
}

func TestBroadcast(t *testing.T) {
	cases := map[string]string{
		"192.168.51.0/24": "192.168.51.255",
		"192.168.0.0/16":  "192.168.255.255",
		"10.0.0.0/25":     "10.0.0.127",
		"10.0.0.0/30":     "10.0.0.3",
	}
	for cidr, want := range cases {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			t.Fatalf("parse %q: %v", cidr, err)
		}
		if got := Broadcast(p).String(); got != want {
			t.Errorf("Broadcast(%q) = %q, want %q", cidr, got, want)
		}
	}
}
