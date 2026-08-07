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
)

// Request is one RPC sent over the socket. Exactly one of the payload fields
// is populated per method.
type Request struct {
	Method Method             `json:"method"`
	Stack  string             `json:"stack,omitempty"`
	Nets   []hostnet.NetSpec  `json:"nets,omitempty"`
	Taps   []hostnet.TapSpec  `json:"taps,omitempty"`
	Ports  []hostnet.PortSpec `json:"ports,omitempty"`
}

// Response is the daemon's reply. Error is empty on success.
type Response struct {
	Error string `json:"error,omitempty"`
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
}

// dispatch runs a request against ops and writes the response to w.
func dispatch(w io.Writer, ops Ops, req Request) error {
	var err error
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
	default:
		err = fmt.Errorf("provisiond: unknown method %q", req.Method)
	}
	resp := Response{}
	if err != nil {
		resp.Error = err.Error()
	}
	return json.NewEncoder(w).Encode(resp)
}
