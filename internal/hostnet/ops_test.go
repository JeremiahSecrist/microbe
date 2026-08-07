package hostnet

import (
	"strings"
	"testing"
)

// TestBridgeName is the red-green gate for Linux IFNAMSIZ=15: bridge names
// must stay ≤15 chars regardless of stack/network length, and be
// deterministic + unique per (stack, net) pair.
func TestBridgeName(t *testing.T) {
	if got := BridgeName("ab", "backend"); got != "br-ab-backend" {
		t.Errorf("BridgeName(short) = %q, want br-ab-backend", got)
	}
	// "br-mystack-backend" is 17, must hash.
	if got := BridgeName("mystack", "backend"); len(got) > 15 {
		t.Errorf("BridgeName(mystack, backend) = %q len %d, want ≤15", got, len(got))
	}

	// The canonical fixture stack+net: readable form would be
	// br-test-net-backend (19 chars) — must collapse to a ≤15-char name.
	long := BridgeName("test-net", "backend")
	if len(long) > 15 {
		t.Errorf("BridgeName(test-net, backend) = %q len %d, want ≤15", long, len(long))
	}
	if !strings.HasPrefix(long, "br-") {
		t.Errorf("BridgeName(test-net, backend) = %q, want br- prefix", long)
	}

	// Deterministic.
	if again := BridgeName("test-net", "backend"); again != long {
		t.Errorf("BridgeName not deterministic: %q vs %q", again, long)
	}

	// Unique per (stack, net) pair even when names collide after hashing.
	a := BridgeName("test-net", "backend")
	b := BridgeName("test-net", "frontend")
	if a == b {
		t.Errorf("distinct networks collided: %q", a)
	}
	c := BridgeName("other", "backend")
	if c == a {
		t.Errorf("distinct stacks collided: %q", a)
	}
}
