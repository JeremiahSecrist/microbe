package hostnet

import (
	"os"
	"strings"
	"testing"

	"microbe/internal/config"
)

const fixture = "../../test/fixtures/networking/projection.json"

func loadFixture(t *testing.T) *config.Compose {
	t.Helper()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate fixture: %v", err)
	}
	return cfg
}

func TestFixtureNetworkingTarget(t *testing.T) {
	cfg := loadFixture(t)

	plan, err := Plan(cfg)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	ips := func(svc, net string) string {
		if m := plan.IPs[svc]; m != nil {
			return m[net]
		}
		return ""
	}

	cases := []struct {
		svc, net, want string
	}{
		{"db", "backend", "192.168.51.2"},
		{"web", "backend", "192.168.51.3"},
		{"web", "frontend", "192.168.50.3"},
		{"jump", "frontend", "192.168.50.2"},
		{"jump", "backend", "192.168.51.4"},
	}
	for _, c := range cases {
		if got := ips(c.svc, c.net); got != c.want {
			t.Errorf("IP %s/%s = %q, want %q", c.svc, c.net, got, c.want)
		}
	}

	if got := plan.IPs["db"]["frontend"]; got != "" {
		t.Errorf("db should not be on frontend, got %q", got)
	}
}

func TestFixtureNetworkingUniqueMACs(t *testing.T) {
	cfg := loadFixture(t)
	plan, err := Plan(cfg)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	seen := map[string]bool{}
	count := 0
	for svc, nets := range plan.MACs {
		for net, mac := range nets {
			count++
			if !validLocalMAC(mac) {
				t.Errorf("invalid MAC %q for %s/%s", mac, svc, net)
			}
			if seen[mac] {
				t.Errorf("duplicate MAC %q (%s/%s)", mac, svc, net)
			}
			seen[mac] = true
		}
	}
	if count != 5 {
		t.Errorf("expected 5 interfaces, got %d", count)
	}
	if plan.MACs["db"]["backend"] != "02:00:00:00:00:01" {
		t.Errorf("db backend MAC = %q, want 02:00:00:00:00:01", plan.MACs["db"]["backend"])
	}
}

func TestFixtureNetworkingDNSOrder(t *testing.T) {
	cfg := loadFixture(t)
	plan, err := Plan(cfg)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	hosts := RenderHosts(plan)
	joined := strings.Join(hosts, "\n")
	for _, want := range []string{
		"192.168.51.2 db",
		"192.168.50.3 web",
		"192.168.51.4 jump",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("hosts file missing %q\n%s", want, joined)
		}
	}
}

func TestNextFreeSkipsBroadcast(t *testing.T) {
	got, err := nextFree("10.0.0.0/30", map[string]bool{"10.0.0.2": true})
	if err == nil {
		t.Fatalf("nextFree returned %q for exhausted /30, want error (broadcast 10.0.0.3 must not be handed out)", got)
	}
}

func validLocalMAC(mac string) bool {
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		return false
	}
	if parts[0] != "02" {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) != 2 {
			return false
		}
		for _, r := range p {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
	}
	return true
}
