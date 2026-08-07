package provisiond

import (
	"errors"
	"fmt"
	"testing"

	"microbe/internal/hostnet"

	"github.com/vishvananda/netlink"
)

// TestTapLinkOwnership is the red-green gate for tap devices cloud-hypervisor
// can reopen: the daemon creates them as root, but the VM attaches as the
// invoking user, so the tap must carry the caller's uid AND IFF_VNET_HDR
// (cloud-hypervisor's virtio-net requirement). netlink's LinkAdd calls
// TUNSETOWNER/TUNSETGROUP unconditionally, so an Owner of 0 pins the tap to
// root and the VM cannot attach.
func TestTapLinkOwnership(t *testing.T) {
	link := tapLink(hostnet.TapSpec{Name: "mvc-test", Owner: 1000})
	tap, ok := link.(*netlink.Tuntap)
	if !ok {
		t.Fatalf("tapLink returned %T, want *netlink.Tuntap", link)
	}
	if tap.Mode != netlink.TUNTAP_MODE_TAP {
		t.Errorf("Mode = %v, want TAP", tap.Mode)
	}
	if tap.Owner != 1000 {
		t.Errorf("Owner = %d, want 1000 (reopen by cloud-hypervisor)", tap.Owner)
	}
	if tap.NonPersist {
		t.Error("NonPersist set; tap must survive the daemon connection")
	}
	if tap.Flags&netlink.TUNTAP_VNET_HDR == 0 {
		t.Errorf("Flags = %v, want IFF_VNET_HDR for cloud-hypervisor virtio-net", tap.Flags)
	}
}

// TestTapNeedsRecreate is the red-green gate for tap ownership reconciliation:
// a tap created by a pre-25b85b3 CLI (or any foreign owner) is root-owned and
// must be recreated so cloud-hypervisor (running as the invoking uid) can
// attach via TUNSETIFF; a tap already owned by the spec's uid must be kept.
func TestTapNeedsRecreate(t *testing.T) {
	spec := hostnet.TapSpec{Name: "mvc-test", Owner: 1000}

	if !tapNeedsRecreate(spec, &netlink.Tuntap{Owner: 0}) {
		t.Error("root-owned tap (Owner=0) vs spec Owner=1000: want recreate")
	}
	if tapNeedsRecreate(spec, &netlink.Tuntap{Owner: 1000}) {
		t.Error("tap already owned by spec Owner=1000: want keep")
	}
}

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
