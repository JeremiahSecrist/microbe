package hostnet

import "testing"

func TestBridgeName(t *testing.T) {
	if got := BridgeName("mystack", "backend"); got != "br-mystack-backend" {
		t.Errorf("BridgeName = %q, want br-mystack-backend", got)
	}
}

func TestSpecTypes(t *testing.T) {
	n := NetSpec{Name: "backend", Gateway: "192.168.51.1", Prefix: 24}
	if n.Name != "backend" || n.Gateway != "192.168.51.1" || n.Prefix != 24 {
		t.Errorf("NetSpec = %+v", n)
	}
	tap := TapSpec{Name: "mvc-web-backend", Bridge: "br-mystack-backend"}
	if tap.Name != "mvc-web-backend" || tap.Bridge != "br-mystack-backend" {
		t.Errorf("TapSpec = %+v", tap)
	}
	p := PortSpec{HostPort: 8080, GuestIP: "192.168.51.2", GuestPort: 80}
	if p.HostPort != 8080 || p.GuestIP != "192.168.51.2" || p.GuestPort != 80 {
		t.Errorf("PortSpec = %+v", p)
	}
}
