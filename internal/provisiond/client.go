package provisiond

import (
	"encoding/json"
	"fmt"
	"net"

	"microbe/internal/hostnet"
)

// client.go is the CLI side: it dials microbe-provisiond over the unix socket
// and sends NetSpec/TapSpec/PortSpec requests for the daemon to apply.
// No privilege is required on the CLI side.

// Client is a connection to microbe-provisiond.
type Client struct {
	conn net.Conn
}

// Dial connects to the daemon's unix socket at path.
func Dial(path string) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("provisiond: dial %s: %w (is microbe-provisiond running?)", path, err)
	}
	return &Client{conn: conn}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// call sends one request and returns the daemon's error, if any.
func (c *Client) call(req Request) error {
	_, err := c.callResp(req)
	return err
}

// callResp sends one request and returns the daemon's full response.
func (c *Client) callResp(req Request) (Response, error) {
	if err := json.NewEncoder(c.conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("provisiond: send request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(c.conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("provisiond: read response: %w", err)
	}
	if resp.Error != "" {
		return Response{}, fmt.Errorf("provisiond: %s", resp.Error)
	}
	return resp, nil
}

// EnsureNetworks asks the daemon to create the bridges and assign gateways.
func (c *Client) EnsureNetworks(stack string, nets []hostnet.NetSpec) error {
	return c.call(Request{Method: MethodEnsureNetworks, Stack: stack, Nets: nets})
}

// EnsureTaps asks the daemon to create and enslave the tap devices.
func (c *Client) EnsureTaps(taps []hostnet.TapSpec) error {
	return c.call(Request{Method: MethodEnsureTaps, Taps: taps})
}

// ApplyPorts asks the daemon to install the DNAT rules.
func (c *Client) ApplyPorts(ports []hostnet.PortSpec) error {
	return c.call(Request{Method: MethodApplyPorts, Ports: ports})
}

// TeardownNetworks asks the daemon to delete the bridges (best-effort).
func (c *Client) TeardownNetworks(stack string, nets []hostnet.NetSpec) error {
	return c.call(Request{Method: MethodTeardownNetworks, Stack: stack, Nets: nets})
}

// TeardownTaps asks the daemon to delete the tap devices (best-effort).
func (c *Client) TeardownTaps(taps []hostnet.TapSpec) error {
	return c.call(Request{Method: MethodTeardownTaps, Taps: taps})
}

// TeardownPorts asks the daemon to remove the DNAT rules (best-effort).
func (c *Client) TeardownPorts(ports []hostnet.PortSpec) error {
	return c.call(Request{Method: MethodTeardownPorts, Ports: ports})
}

// TeardownLinks asks the daemon to delete links by exact name (best-effort).
func (c *Client) TeardownLinks(links []string) error {
	return c.call(Request{Method: MethodTeardownLinks, Links: links})
}

// EnsurePrefix asks the daemon for the host's persisted ULA /64, generating
// one on first call.
func (c *Client) EnsurePrefix() (string, error) {
	resp, err := c.callResp(Request{Method: MethodEnsurePrefix})
	if err != nil {
		return "", err
	}
	return resp.Prefix, nil
}

// ApplyRules asks the daemon to install the forward-chain accept rules.
func (c *Client) ApplyRules(rules []hostnet.RuleSpec) error {
	return c.call(Request{Method: MethodApplyRules, Rules: rules})
}

// TeardownRules asks the daemon to remove the forward-chain accept rules
// (best-effort).
func (c *Client) TeardownRules(rules []hostnet.RuleSpec) error {
	return c.call(Request{Method: MethodTeardownRules, Rules: rules})
}

// ApplyHostAccess asks the daemon to install the output-chain host->guest
// accept rules for the given opted-in services.
func (c *Client) ApplyHostAccess(specs []hostnet.HostAccessSpec) error {
	return c.call(Request{Method: MethodApplyHostAccess, HostAccess: specs})
}

// TeardownHostAccess asks the daemon to remove the output-chain host->guest
// accept rules (best-effort).
func (c *Client) TeardownHostAccess(specs []hostnet.HostAccessSpec) error {
	return c.call(Request{Method: MethodTeardownHostAccess, HostAccess: specs})
}

// ApplyHealthAccess asks the daemon to install the output-chain accept rules
// for each service's declared healthcheck port.
func (c *Client) ApplyHealthAccess(specs []hostnet.HealthAccessSpec) error {
	return c.call(Request{Method: MethodApplyHealthAccess, HealthAccess: specs})
}

// TeardownHealthAccess asks the daemon to remove the output-chain
// healthcheck-port accept rules (best-effort).
func (c *Client) TeardownHealthAccess(specs []hostnet.HealthAccessSpec) error {
	return c.call(Request{Method: MethodTeardownHealthAccess, HealthAccess: specs})
}
