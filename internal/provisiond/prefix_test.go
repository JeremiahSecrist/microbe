package provisiond

import (
	"net/netip"
	"path/filepath"
	"testing"

	"microbe/internal/state"
)

func withTestHostStatePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host-state.json")
	orig := hostStatePath
	hostStatePath = path
	t.Cleanup(func() { hostStatePath = orig })
	return path
}

func TestEnsurePrefixGeneratesULA(t *testing.T) {
	withTestHostStatePath(t)

	prefix, err := (NetOps{}).EnsurePrefix()
	if err != nil {
		t.Fatalf("EnsurePrefix: %v", err)
	}
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		t.Fatalf("prefix %q not parseable: %v", prefix, err)
	}
	if p.Bits() != 64 {
		t.Errorf("prefix bits = %d, want 64", p.Bits())
	}
	addr := p.Addr().As16()
	if addr[0] != 0xfd {
		t.Errorf("prefix %s does not start with fd (RFC 4193 locally-assigned ULA)", prefix)
	}
}

func TestEnsurePrefixIsStableAcrossCalls(t *testing.T) {
	withTestHostStatePath(t)

	first, err := (NetOps{}).EnsurePrefix()
	if err != nil {
		t.Fatalf("first EnsurePrefix: %v", err)
	}
	second, err := (NetOps{}).EnsurePrefix()
	if err != nil {
		t.Fatalf("second EnsurePrefix: %v", err)
	}
	if first != second {
		t.Errorf("prefix changed across calls: %q -> %q", first, second)
	}
}

func TestEnsurePrefixHonorsExistingState(t *testing.T) {
	path := withTestHostStatePath(t)
	hs := &state.HostState{ULAPrefix: "fd00:aaaa:bbbb::/64"}
	if err := hs.Save(path); err != nil {
		t.Fatalf("save host state: %v", err)
	}

	got, err := (NetOps{}).EnsurePrefix()
	if err != nil {
		t.Fatalf("EnsurePrefix: %v", err)
	}
	if got != "fd00:aaaa:bbbb::/64" {
		t.Errorf("EnsurePrefix = %q, want the pre-seeded prefix unchanged", got)
	}
}
