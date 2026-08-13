package hostnet

import (
	"net/netip"
	"os"
	"strings"
	"testing"

	"microbe/internal/config"
	"microbe/internal/lockfile"
)

const fixture = "../../test/fixtures/networking/projection.json"
const testPrefix = "fd00:1234:5678::/64"

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

func newLock() *lockfile.Lock {
	return &lockfile.Lock{Prefix: testPrefix, Services: map[string]string{}}
}

func TestFixtureNetworkingStaticAddrs(t *testing.T) {
	cfg := loadFixture(t)
	plan, err := Plan(cfg, newLock())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	cases := []struct{ svc, want string }{
		{"db", "fd00:1234:5678::2"},
		{"web", "fd00:1234:5678::3"},
	}
	for _, c := range cases {
		if got := plan.Addrs[c.svc]; got != c.want {
			t.Errorf("addr %s = %q, want %q", c.svc, got, c.want)
		}
	}
}

func TestFixtureNetworkingAutoAddrWithinPrefix(t *testing.T) {
	cfg := loadFixture(t)
	plan, err := Plan(cfg, newLock())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	prefix := netip.MustParsePrefix(testPrefix)
	addr, err := netip.ParseAddr(plan.Addrs["jump"])
	if err != nil {
		t.Fatalf("jump addr %q: %v", plan.Addrs["jump"], err)
	}
	if !prefix.Contains(addr) {
		t.Errorf("jump addr %s not within %s", addr, prefix)
	}
	for svc, addrStr := range plan.Addrs {
		for other, otherStr := range plan.Addrs {
			if svc != other && addrStr == otherStr {
				t.Errorf("services %s and %s share addr %s", svc, other, addrStr)
			}
		}
	}
}

func TestPlanReusesLockedAddrs(t *testing.T) {
	cfg := loadFixture(t)
	lock := newLock()

	first, err := Plan(cfg, lock)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	jumpAddr := first.Addrs["jump"]
	if jumpAddr == "" {
		t.Fatal("jump got no address on first plan")
	}
	if len(lock.Services) != 3 {
		t.Errorf("lock has %d services after first plan, want 3", len(lock.Services))
	}

	second, err := Plan(cfg, lock)
	if err != nil {
		t.Fatalf("re-plan: %v", err)
	}
	for svc, addr := range first.Addrs {
		if second.Addrs[svc] != addr {
			t.Errorf("service %s addr changed across plans: %q -> %q", svc, addr, second.Addrs[svc])
		}
	}
}

func TestFixtureNetworkingUniqueMACs(t *testing.T) {
	cfg := loadFixture(t)
	plan, err := Plan(cfg, newLock())
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

func TestFixtureNetworkingHosts(t *testing.T) {
	cfg := loadFixture(t)
	plan, err := Plan(cfg, newLock())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	hosts := RenderHosts(plan)
	joined := strings.Join(hosts, "\n")
	for _, want := range []string{
		"fd00:1234:5678::2 db",
		"fd00:1234:5678::3 web",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("hosts file missing %q\n%s", want, joined)
		}
	}
}

func TestPlanRequiresPrefix(t *testing.T) {
	cfg := loadFixture(t)
	_, err := Plan(cfg, &lockfile.Lock{})
	if err == nil {
		t.Fatal("want error for empty lock prefix")
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
