package state

import "testing"

func TestNewIDFormatAndUniqueness(t *testing.T) {
	a := NewID()
	b := NewID()

	if len(a) != 36 {
		t.Errorf("NewID() length = %d, want 36 (got %q)", len(a), a)
	}
	for i, c := range a {
		isDash := i == 8 || i == 13 || i == 18 || i == 23
		if isDash && c != '-' {
			t.Errorf("NewID() = %q, want dash at index %d", a, i)
		}
	}
	if a[14] != '4' {
		t.Errorf("NewID() = %q, want version nibble 4 at index 14", a)
	}

	if a == b {
		t.Errorf("NewID() returned the same value twice: %q", a)
	}
}
