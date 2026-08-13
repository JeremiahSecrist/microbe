package flakegen

import (
	"reflect"
	"testing"

	"microbe/internal/config"
	"microbe/internal/hostnet"
	"microbe/internal/lockfile"
)

const testPrefix = "fd00:1234:5678::/64"

func newTestLock() *lockfile.Lock {
	return &lockfile.Lock{Prefix: testPrefix, Services: map[string]string{}}
}

func fixtureConfig() *config.Compose {
	return &config.Compose{
		SchemaVersion: 1,
		Name:          "test-net",
		Networks: map[string]config.Network{
			"frontend": {},
			"backend":  {},
		},
		Services: map[string]config.Service{
			"db": {
				Networks: []config.Attach{{Name: "backend", Addr: "fd00:1234:5678::2"}},
			},
			"web": {
				Networks: []config.Attach{
					{Name: "backend", Addr: "fd00:1234:5678::3"},
					{Name: "frontend", Addr: "fd00:1234:5678::3"},
				},
			},
			"jump": {
				Networks: []config.Attach{
					{Name: "frontend", Addr: "fd00:1234:5678::4"},
					{Name: "backend", Addr: "fd00:1234:5678::4"},
				},
			},
		},
	}
}

func TestFromConfigStackInternal(t *testing.T) {
	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "airgap-test",
		Networks: map[string]config.Network{
			"public": {},
			"secure": {Internal: true},
		},
		Services: map[string]config.Service{
			"db": {Networks: []config.Attach{{Name: "secure"}}},
		},
	}
	plan, err := hostnet.Plan(cfg, newTestLock())
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan, testPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if st.Internal["secure"] != true {
		t.Errorf("st.Internal[secure] = %v, want true", st.Internal["secure"])
	}
	if st.Internal["public"] != false {
		t.Errorf("st.Internal[public] = %v, want false", st.Internal["public"])
	}
}

func TestFromConfigStack(t *testing.T) {
	cfg := fixtureConfig()
	plan, err := hostnet.Plan(cfg, newTestLock())
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan, testPrefix)
	if err != nil {
		t.Fatal(err)
	}

	if len(st.Services) != 3 {
		t.Fatalf("Services = %d, want 3", len(st.Services))
	}

	db := st.Services["db"]
	if db.CID != 3 {
		t.Errorf("db.CID = %d, want 3", db.CID)
	}
	if !reflect.DeepEqual(db.Networks, []string{"backend"}) {
		t.Errorf("db.Networks = %v, want [backend]", db.Networks)
	}
	if db.Prefix != 64 {
		t.Errorf("db.Prefix = %d, want 64", db.Prefix)
	}
	if db.Gateway != "fd00:1234:5678::1" {
		t.Errorf("db.Gateway = %q, want fd00:1234:5678::1", db.Gateway)
	}
	if db.Addr != "fd00:1234:5678::2" {
		t.Errorf("db.Addr = %q, want fd00:1234:5678::2", db.Addr)
	}
	if db.MACs["backend"] != "02:00:00:00:00:01" {
		t.Errorf("db.MACs[backend] = %q, want 02:00:00:00:00:01", db.MACs["backend"])
	}

	if web := st.Services["web"]; web.CID != 5 {
		t.Errorf("web.CID = %d, want 5", web.CID)
	}
	if jump := st.Services["jump"]; jump.CID != 4 {
		t.Errorf("jump.CID = %d, want 4", jump.CID)
	}
}

func TestFromConfigBuildTarget(t *testing.T) {
	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "os-test",
		Networks:      map[string]config.Network{"n": {}},
		Services: map[string]config.Service{
			"a": {OS: "nixos", Networks: []config.Attach{{Name: "n"}}},
			"b": {OS: "finix", Networks: []config.Attach{{Name: "n"}}},
		},
	}
	plan, err := hostnet.Plan(cfg, newTestLock())
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan, testPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := st.Services["a"].OS, "nixos"; got != want {
		t.Errorf("a.OS = %q, want %q", got, want)
	}
	if got, want := st.Services["a"].BuildTarget, ".#nixosConfigurations.a.config.microvm.declaredRunner"; got != want {
		t.Errorf("a.BuildTarget = %q, want %q", got, want)
	}
	if got, want := st.Services["b"].OS, "finix"; got != want {
		t.Errorf("b.OS = %q, want %q", got, want)
	}
	if got, want := st.Services["b"].BuildTarget, ".#finixConfigurations.b.config.microbe.qemuRunner"; got != want {
		t.Errorf("b.BuildTarget = %q, want %q", got, want)
	}
}

func TestStackHosts(t *testing.T) {
	cfg := fixtureConfig()
	plan, err := hostnet.Plan(cfg, newTestLock())
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan, testPrefix)
	if err != nil {
		t.Fatal(err)
	}

	got := st.Hosts()
	want := []Host{
		{IP: "fd00:1234:5678::2", Names: []string{"db"}},
		{IP: "fd00:1234:5678::4", Names: []string{"jump"}},
		{IP: "fd00:1234:5678::3", Names: []string{"web"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() =\n  %v\nwant:\n  %v", got, want)
	}
}
