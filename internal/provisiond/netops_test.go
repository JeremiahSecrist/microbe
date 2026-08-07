package provisiond

import (
	"errors"
	"fmt"
	"testing"

	"github.com/vishvananda/netlink"
)

// TestIsLinkNotFound is the red-green gate for the bridge/tap "create if
// missing" path. netlink.LinkByName returns LinkNotFoundError by VALUE (not
// pointer), so matching with errors.As against *LinkNotFoundError silently
// misses and turns a missing link into a hard error instead of a create.
func TestIsLinkNotFound(t *testing.T) {
	_, err := netlink.LinkByName("microbe-no-such-link-0000")
	if err == nil {
		t.Fatal("netlink.LinkByName on a nonexistent link returned no error")
	}
	if _, ok := err.(netlink.LinkNotFoundError); !ok {
		t.Fatalf("expected value-type LinkNotFoundError, got %T", err)
	}

	if !isLinkNotFound(err) {
		t.Errorf("value-type LinkNotFoundError not recognized: %v", err)
	}
	if !isLinkNotFound(fmt.Errorf("wrap: %w", err)) {
		t.Error("wrapped value-type LinkNotFoundError not recognized")
	}
	if isLinkNotFound(errors.New("no such interface")) {
		t.Error("unrelated error wrongly treated as link-not-found")
	}
	if isLinkNotFound(nil) {
		t.Error("nil error wrongly treated as link-not-found")
	}
}
