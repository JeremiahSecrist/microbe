package flakegen

import "testing"

func TestNetworkdUnitForDB(t *testing.T) {
	u := NetworkdUnit("02:00:00:00:00:01", "192.168.51.2", 24, "192.168.51.1")
	if u.MAC != "02:00:00:00:00:01" {
		t.Errorf("MAC = %q", u.MAC)
	}
	if u.Address != "192.168.51.2/24" {
		t.Errorf("Address = %q, want 192.168.51.2/24", u.Address)
	}
	if u.Gateway != "192.168.51.1" {
		t.Errorf("Gateway = %q, want 192.168.51.1", u.Gateway)
	}
}

func TestGateway(t *testing.T) {
	cases := map[string]string{
		"192.168.51.0/24": "192.168.51.1",
		"192.168.0.0/16":  "192.168.0.1",
		"10.10.10.0/24":   "10.10.10.1",
	}
	for cidr, want := range cases {
		got, err := Gateway(cidr)
		if err != nil {
			t.Errorf("Gateway(%q) error: %v", cidr, err)
			continue
		}
		if got != want {
			t.Errorf("Gateway(%q) = %q, want %q", cidr, got, want)
		}
	}
}
