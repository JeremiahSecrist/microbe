package hostnet

import (
	"crypto/sha256"
	"encoding/hex"
)

// maxIfNameLen is Linux's interface name length cap (IFNAMSIZ, including the
// NUL terminator leaves 15 usable bytes). Tap and bridge names must fit
// within this.
const maxIfNameLen = 15

// bridgePrefix is prepended to every bridge name, readable or hashed.
const bridgePrefix = "br-"

// hashedNameLen is the hex digest length used for a hashed bridge name, sized
// so bridgePrefix+hash stays within maxIfNameLen.
const hashedNameLen = maxIfNameLen - len(bridgePrefix)

// Host resource specs. The lifecycle commands (M4) build these from the
// network plan and stack, then ship them to the microbe-provisiond daemon,
// which applies them via netlink/nftables.
//
// CONTRACT: internal/cmd and internal/provisiond depend on the exact type
// and field names in this file. Do not rename them without updating those
// callers.

// NetSpec is the one bridge a stack provisions: br-<stack>, carrying the
// host's flat-network gateway address. Every service in every stack shares
// the same Gateway/Prefix (the host's persisted ULA /64, see
// internal/netutil.V6Gateway) -- unlike the old per-network-subnet model,
// there is exactly one NetSpec per stack, not one per declared network.
type NetSpec struct {
	Gateway string // e.g. "fd7a:3c9e:1122::1"
	Prefix  int    // e.g. 64
}

// TapSpec is one tap interface, enslaved to its stack's one bridge.
type TapSpec struct {
	Name   string // tap id, e.g. "mvc-...-backend" (≤15 chars)
	Bridge string // bridge id, e.g. "br-<stack>"
	Owner  int    // uid that may reopen the tap (the VM process), 0 = root
	Group  int    // gid that may reopen the tap; the kernel checks this
	// independently of Owner (TUNSETGROUP), so a zero-value Group pins the
	// tap to the root group even when Owner is correct.

	// MultiQueue must match microvm.nix's cloud-hypervisor.nix `tapMultiQueue
	// = vcpu > 1`: true only when the guest's vcpu count is > 1. cloud-
	// hypervisor's net_util::open_tap::check_mq_support rejects a MISMATCH
	// in either direction -- a single-queue tap for a multi-vcpu guest fails
	// with MultiQueueNoTapSupport, and a multiqueue-capable tap for a
	// single-vcpu guest (which never passes --net num_queues) fails with
	// MultiQueueNoDeviceSupport. The tap's IFF_MULTI_QUEUE flag must track
	// this exactly, not be set unconditionally.
	MultiQueue bool
}

// PortSpec is one published port: DNAT from HostPort to the guest's IPv6
// address.
type PortSpec struct {
	HostPort  int
	GuestIP   string
	GuestPort int
}

// RuleSpec is one resolved entry from config.Compose.Rules: From may reach
// To on Port (0 = every port) over Proto. Default-deny is enforced by the
// forward chain's policy (see provisiond's EnsureRules); a RuleSpec is the
// one exception carved out of that policy.
type RuleSpec struct {
	From, To string // resolved IPv6 addresses (internal/lockfile)
	Proto    string // "tcp" or "udp"
	Port     int    // 0 = every port for Proto
}

// BridgeName returns the host bridge name for a stack: readable "br-<stack>"
// when it fits, else a deterministic 15-char hash-based name (Linux
// interface names are capped at IFNAMSIZ=15). One bridge serves every
// network a stack declares -- Network is a pure label in the flat-address
// model (see config.Network), so there is nothing left to key a bridge on
// beyond the stack itself.
func BridgeName(stack string) string {
	full := bridgePrefix + stack
	if len(full) <= maxIfNameLen {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return bridgePrefix + hex.EncodeToString(sum[:])[:hashedNameLen]
}
