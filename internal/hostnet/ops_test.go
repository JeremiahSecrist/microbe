package hostnet

import (
	"strings"
	"testing"
)

// TestBridgeName is the red-green gate for Linux IFNAMSIZ=15: bridge names
// must stay ≤15 chars regardless of stack length, and be deterministic +
// unique per stack (one bridge per stack now, not one per network -- see
// BridgeName's doc comment).
func TestBridgeName(t *testing.T) {
	if got := BridgeName("ab"); got != "br-ab" {
		t.Errorf("BridgeName(short) = %q, want br-ab", got)
	}

	// The canonical fixture stack: readable form would be
	// br-test-net-of-unusual-length (well over 15 chars) — must collapse to
	// a ≤15-char name.
	long := BridgeName("test-net-of-unusual-length")
	if len(long) > 15 {
		t.Errorf("BridgeName(long stack) = %q len %d, want ≤15", long, len(long))
	}
	if !strings.HasPrefix(long, "br-") {
		t.Errorf("BridgeName(long stack) = %q, want br- prefix", long)
	}

	// Deterministic.
	if again := BridgeName("test-net-of-unusual-length"); again != long {
		t.Errorf("BridgeName not deterministic: %q vs %q", again, long)
	}

	// Unique per stack even when names collide after hashing.
	a := BridgeName("test-net-of-unusual-length")
	b := BridgeName("other-test-net-of-unusual-length")
	if a == b {
		t.Errorf("distinct stacks collided: %q", a)
	}
}
