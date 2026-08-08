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
	link := tapLink(hostnet.TapSpec{Name: "mvc-test", Owner: 1000, Group: 100, MultiQueue: true})
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
	if tap.Group != 100 {
		t.Errorf("Group = %d, want 100 (kernel checks TUNSETGROUP independently of TUNSETOWNER)", tap.Group)
	}
	if tap.NonPersist {
		t.Error("NonPersist set; tap must survive the daemon connection")
	}
	if tap.Flags&netlink.TUNTAP_VNET_HDR == 0 {
		t.Errorf("Flags = %v, want IFF_VNET_HDR for cloud-hypervisor virtio-net", tap.Flags)
	}
	if tap.Flags&netlink.TUNTAP_MULTI_QUEUE == 0 {
		t.Errorf("Flags = %v, want IFF_MULTI_QUEUE: cloud-hypervisor sizes net queues to vcpu count and refuses a single-queue tap once vcpus > 1", tap.Flags)
	}
}

// TestTapLinkSingleQueue is the red-green gate for the regression cloud-
// hypervisor's own net_util::open_tap::check_mq_support enforces: a tap
// that advertises IFF_MULTI_QUEUE but whose guest was NOT configured with
// multiple net queues (vcpu == 1, so microvm.nix's cloud-hypervisor.nix
// never passes `num_queues` on --net) is rejected at boot with
// "MultiQueueNoDeviceSupport" -- the mirror image of MultiQueueNoTapSupport.
// tapLink must only request IFF_MULTI_QUEUE when the spec says the guest
// actually wants multiple queues.
func TestTapLinkSingleQueue(t *testing.T) {
	link := tapLink(hostnet.TapSpec{Name: "mvc-test", Owner: 1000, Group: 100, MultiQueue: false})
	tap, ok := link.(*netlink.Tuntap)
	if !ok {
		t.Fatalf("tapLink returned %T, want *netlink.Tuntap", link)
	}
	if tap.Flags&netlink.TUNTAP_VNET_HDR == 0 {
		t.Errorf("Flags = %v, want IFF_VNET_HDR for cloud-hypervisor virtio-net", tap.Flags)
	}
	if tap.Flags&netlink.TUNTAP_MULTI_QUEUE != 0 {
		t.Errorf("Flags = %v, want IFF_MULTI_QUEUE unset: single-vcpu guests don't pass num_queues, so cloud-hypervisor rejects a multiqueue-capable tap with MultiQueueNoDeviceSupport", tap.Flags)
	}
}

// TestTapNeedsRecreate is the red-green gate for tap ownership reconciliation:
// a tap created by a pre-25b85b3 CLI (or any foreign owner) is root-owned and
// must be recreated so cloud-hypervisor (running as the invoking uid) can
// attach via TUNSETIFF; a tap already owned by the spec's uid must be kept.
func TestTapNeedsRecreate(t *testing.T) {
	spec := hostnet.TapSpec{Name: "mvc-test", Owner: 1000, Group: 100, MultiQueue: true}

	if !tapNeedsRecreate(spec, &netlink.Tuntap{Owner: 0, Group: 100}) {
		t.Error("root-owned tap (Owner=0) vs spec Owner=1000: want recreate")
	}
	if !tapNeedsRecreate(spec, &netlink.Tuntap{Owner: 1000, Group: 0}) {
		t.Error("root-group tap (Group=0) vs spec Group=100: want recreate")
	}
	if tapNeedsRecreate(spec, &netlink.Tuntap{Owner: 1000, Group: 100, Flags: netlink.TUNTAP_MULTI_QUEUE_DEFAULTS | netlink.TUNTAP_VNET_HDR}) {
		t.Error("tap already owned by spec Owner=1000/Group=100 with multiqueue set: want keep")
	}
	if !tapNeedsRecreate(spec, &netlink.Tuntap{Owner: 1000, Group: 100, Flags: netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR}) {
		t.Error("single-queue tap (no IFF_MULTI_QUEUE) with matching owner: want recreate (fixes MultiQueueNoTapSupport on existing hosts)")
	}

	// Mirror image: a single-vcpu spec (MultiQueue: false) whose existing tap
	// is left over from a multiqueue-capable creation (e.g. a prior daemon
	// build that set IFF_MULTI_QUEUE unconditionally) must also be recreated,
	// or cloud-hypervisor rejects it with MultiQueueNoDeviceSupport.
	singleQueueSpec := hostnet.TapSpec{Name: "mvc-test", Owner: 1000, Group: 100, MultiQueue: false}
	if !tapNeedsRecreate(singleQueueSpec, &netlink.Tuntap{Owner: 1000, Group: 100, Flags: netlink.TUNTAP_MULTI_QUEUE_DEFAULTS | netlink.TUNTAP_VNET_HDR}) {
		t.Error("multiqueue-capable tap with matching owner but spec.MultiQueue=false: want recreate (fixes MultiQueueNoDeviceSupport)")
	}
	if tapNeedsRecreate(singleQueueSpec, &netlink.Tuntap{Owner: 1000, Group: 100, Flags: netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR}) {
		t.Error("single-queue tap with matching owner and spec.MultiQueue=false: want keep")
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
