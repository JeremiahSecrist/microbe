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
	if err := json.NewEncoder(c.conn).Encode(req); err != nil {
		return fmt.Errorf("provisiond: send request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(c.conn).Decode(&resp); err != nil {
		return fmt.Errorf("provisiond: read response: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("provisiond: %s", resp.Error)
	}
	return nil
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
