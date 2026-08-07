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
