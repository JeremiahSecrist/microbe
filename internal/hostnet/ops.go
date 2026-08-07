package hostnet

import (
	"crypto/sha256"
	"encoding/hex"
)

// Host resource specs. The lifecycle commands (M4) build these from the
// network plan and stack, then ship them to the microbe-provisiond daemon,
// which applies them via netlink/nftables.
//
// CONTRACT: internal/cmd and internal/provisiond depend on the exact type
// and field names in this file. Do not rename them without updating those
// callers.

// NetSpec is one network to provision on the host: a bridge br-<stack>-<net>
// carrying the gateway address.
type NetSpec struct {
	Name    string // network name, e.g. "backend"
	Gateway string // bridge address, e.g. "192.168.51.1"
	Prefix  int    // gateway prefix length, e.g. 24
}

// TapSpec is one tap interface, enslaved to its network's bridge.
type TapSpec struct {
	Name   string // tap id, e.g. "mvc-...-backend" (≤15 chars)
	Bridge string // bridge id, e.g. "br-<stack>-backend"
	Owner  int    // uid that may reopen the tap (the VM process), 0 = root
	Group  int    // gid that may reopen the tap; the kernel checks this
	// independently of Owner (TUNSETGROUP), so a zero-value Group pins the
	// tap to the root group even when Owner is correct.
}

// PortSpec is one published port: DNAT from HostPort to the guest.
type PortSpec struct {
	HostPort  int
	GuestIP   string
	GuestPort int
}

// BridgeName returns the host bridge name for a stack+network pair: readable
// "br-<stack>-<net>" when it fits, else a deterministic 15-char hash-based
// name (Linux interface names are capped at IFNAMSIZ=15).
func BridgeName(stack, net string) string {
	full := "br-" + stack + "-" + net
	if len(full) <= 15 {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return "br-" + hex.EncodeToString(sum[:])[:12]
}
