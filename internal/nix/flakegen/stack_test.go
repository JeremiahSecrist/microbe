package flakegen

import (
	"reflect"
	"testing"

	"microbe/internal/config"
	"microbe/internal/hostnet"
)

func fixtureConfig() *config.Compose {
	return &config.Compose{
		SchemaVersion: 1,
		Name:          "test-net",
		Networks: map[string]config.Network{
			"frontend": {Subnet: "192.168.50.0/24"},
			"backend":  {Subnet: "192.168.51.0/24"},
		},
		Services: map[string]config.Service{
			"db": {
				Networks: []config.Attach{{Name: "backend", IP: "192.168.51.2"}},
			},
			"web": {
				Networks: []config.Attach{
					{Name: "backend", IP: "192.168.51.3"},
					{Name: "frontend", IP: "192.168.50.3"},
				},
			},
			"jump": {
				Networks: []config.Attach{{Name: "frontend"}, {Name: "backend"}},
			},
		},
	}
}

func TestFromConfigStackInternal(t *testing.T) {
	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "airgap-test",
		Networks: map[string]config.Network{
			"public": {Subnet: "192.168.60.0/24"},
			"secure": {Subnet: "192.168.61.0/24", Internal: true},
		},
		Services: map[string]config.Service{
			"db": {Networks: []config.Attach{{Name: "secure"}}},
		},
	}
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan)
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
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan)
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
	if db.Prefix["backend"] != 24 {
		t.Errorf("db.Prefix[backend] = %d, want 24", db.Prefix["backend"])
	}
	if db.Gateway["backend"] != "192.168.51.1" {
		t.Errorf("db.Gateway[backend] = %q, want 192.168.51.1", db.Gateway["backend"])
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
		Networks:      map[string]config.Network{"n": {Subnet: "192.168.70.0/24"}},
		Services: map[string]config.Service{
			"a": {OS: "nixos", Networks: []config.Attach{{Name: "n"}}},
			"b": {OS: "finix", Networks: []config.Attach{{Name: "n"}}},
		},
	}
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan)
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
	plan, err := hostnet.Plan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := FromConfig(cfg, plan)
	if err != nil {
		t.Fatal(err)
	}

	got := st.Hosts()
	want := []Host{
		{IP: "192.168.51.2", Names: []string{"db", "db.backend"}},
		{IP: "192.168.51.4", Names: []string{"jump", "jump.backend"}},
		{IP: "192.168.50.2", Names: []string{"jump", "jump.frontend"}},
		{IP: "192.168.51.3", Names: []string{"web", "web.backend"}},
		{IP: "192.168.50.3", Names: []string{"web", "web.frontend"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() =\n  %v\nwant:\n  %v", got, want)
	}
}
