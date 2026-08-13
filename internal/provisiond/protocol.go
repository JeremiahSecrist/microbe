package provisiond

import (
	"encoding/json"
	"fmt"
	"io"

	"microbe/internal/hostnet"
)

// protocol.go defines the request/response types and the Ops interface that
// microbe-provisiond serves over its unix socket. The CLI builds
// hostnet.{Net,Tap,Port}Spec values and ships them to the root daemon, which
// applies them via netlink/nftables.

// SocketPath is where microbe-provisiond listens (and the CLI dials). Owned
// root:microbe mode 0660 by the systemd socket unit.
const SocketPath = "/run/microbe.sock"

// Method is a RPC the daemon accepts.
type Method string

const (
	MethodEnsureNetworks   Method = "ensure_networks"
	MethodEnsureTaps       Method = "ensure_taps"
	MethodApplyPorts       Method = "apply_ports"
	MethodTeardownNetworks Method = "teardown_networks"
	MethodTeardownTaps     Method = "teardown_taps"
	MethodTeardownPorts    Method = "teardown_ports"
	MethodTeardownLinks    Method = "teardown_links"
	// MethodEnsurePrefix asks the daemon for the host's persisted ULA /64,
	// generating one on first call. No request payload; the prefix comes
	// back in Response.Prefix.
	MethodEnsurePrefix Method = "ensure_prefix"
)

// Request is one RPC sent over the socket. Exactly one of the payload fields
// is populated per method.
type Request struct {
	Method Method             `json:"method"`
	Stack  string             `json:"stack,omitempty"`
	Nets   []hostnet.NetSpec  `json:"nets,omitempty"`
	Taps   []hostnet.TapSpec  `json:"taps,omitempty"`
	Ports  []hostnet.PortSpec `json:"ports,omitempty"`
	Links  []string           `json:"links,omitempty"`
}

// Response is the daemon's reply. Error is empty on success. Prefix is only
// populated by MethodEnsurePrefix.
type Response struct {
	Error  string `json:"error,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

// Ops is the privileged operation set the server performs. The concrete
// implementation (NetOps) talks netlink/nftables; tests substitute a fake.
type Ops interface {
	EnsureNetworks(stack string, nets []hostnet.NetSpec) error
	EnsureTaps(taps []hostnet.TapSpec) error
	ApplyPorts(ports []hostnet.PortSpec) error
	TeardownNetworks(stack string, nets []hostnet.NetSpec) error
	TeardownTaps(taps []hostnet.TapSpec) error
	TeardownPorts(ports []hostnet.PortSpec) error
	// TeardownLinks deletes links by exact name. Used to sweep orphaned
	// devices recorded in state.json that no current config still names.
	TeardownLinks(links []string) error
	// EnsurePrefix returns the host's persisted ULA /64, generating one on
	// first call (see prefix.go).
	EnsurePrefix() (string, error)
}

// dispatch runs a request against ops and writes the response to w.
func dispatch(w io.Writer, ops Ops, req Request) error {
	var err error
	resp := Response{}
	switch req.Method {
	case MethodEnsureNetworks:
		err = ops.EnsureNetworks(req.Stack, req.Nets)
	case MethodEnsureTaps:
		err = ops.EnsureTaps(req.Taps)
	case MethodApplyPorts:
		err = ops.ApplyPorts(req.Ports)
	case MethodTeardownNetworks:
		err = ops.TeardownNetworks(req.Stack, req.Nets)
	case MethodTeardownTaps:
		err = ops.TeardownTaps(req.Taps)
	case MethodTeardownPorts:
		err = ops.TeardownPorts(req.Ports)
	case MethodTeardownLinks:
		err = ops.TeardownLinks(req.Links)
	case MethodEnsurePrefix:
		resp.Prefix, err = ops.EnsurePrefix()
	default:
		err = fmt.Errorf("provisiond: unknown method %q", req.Method)
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return json.NewEncoder(w).Encode(resp)
}
