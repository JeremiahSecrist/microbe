package provisiond

import (
	"crypto/rand"
	"fmt"
	"net/netip"
	"sync"

	"microbe/internal/state"
)

// prefix.go generates and persists the host's ULA /64: one per host, shared
// by every stack, so addresses drawn from it (see internal/lockfile) never
// collide across stacks. Generated once, on first EnsurePrefix call, and
// never changed afterward.

// hostStatePath is the state.HostStatePath, indirected only so tests can
// point it at a temp file.
var hostStatePath = state.HostStatePath

// prefixMu serializes EnsurePrefix so two concurrent first-`up` connections
// (server.go handles each connection on its own goroutine) can't both
// observe "no prefix yet" and generate two different ones.
var prefixMu sync.Mutex

// EnsurePrefix returns the host's persisted ULA /64, generating and saving
// one via crypto/rand if this is the first call on this host.
func (NetOps) EnsurePrefix() (string, error) {
	prefixMu.Lock()
	defer prefixMu.Unlock()

	hs, err := state.LoadHostState(hostStatePath)
	if err != nil {
		return "", fmt.Errorf("provisiond: load host state: %w", err)
	}
	if hs.ULAPrefix != "" {
		return hs.ULAPrefix, nil
	}

	prefix, err := generateULAPrefix()
	if err != nil {
		return "", fmt.Errorf("provisiond: generate ULA prefix: %w", err)
	}
	hs.ULAPrefix = prefix
	if err := hs.Save(hostStatePath); err != nil {
		return "", fmt.Errorf("provisiond: save host state: %w", err)
	}
	return prefix, nil
}

// generateULAPrefix builds a random RFC 4193 locally-assigned /64:
// 0xfd followed by 40 random bits (the "global ID"), a zero 16-bit subnet
// ID (this host only ever uses the one /64), and a masked /64 boundary.
func generateULAPrefix() (string, error) {
	var addr [16]byte
	addr[0] = 0xfd
	if _, err := rand.Read(addr[1:6]); err != nil {
		return "", err
	}
	// addr[6:8] (subnet ID) and addr[8:16] (host bits) stay zero: the
	// prefix itself, not any one host's address within it.
	prefix := netip.PrefixFrom(netip.AddrFrom16(addr), 64)
	return prefix.String(), nil
}
