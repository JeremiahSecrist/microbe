package vsockexec

import (
	"fmt"
	"io"
	"net"
	"strings"
)

// maxHandshakeLine bounds the hybrid-vsock handshake reply: a guard
// against a misbehaving peer never sending '\n', not a real protocol limit.
const maxHandshakeLine = 256

// DialHybridVsock reaches the guest agent listening on vsock port through
// cloud-hypervisor's own vsock device: cloud-hypervisor exposes a host-side
// UNIX socket (udsPath, "notify.vsock" in each service's run dir) that
// implements the Firecracker-style "hybrid vsock" handshake -- connect to
// the UDS, send "CONNECT <port>\n", and on success get back "OK ...\n",
// after which the connection is a raw duplex byte stream to whatever's
// listening on that port inside the guest. This is what lets an
// unprivileged host process reach a guest's AF_VSOCK listener without the
// vhost_vsock kernel module or any host-side network exposure at all.
func DialHybridVsock(udsPath string, port uint32) (net.Conn, error) {
	conn, err := net.Dial("unix", udsPath)
	if err != nil {
		return nil, fmt.Errorf("vsockexec: dial %s: %w", udsPath, err)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsockexec: send CONNECT: %w", err)
	}
	line, err := readLine(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsockexec: read handshake reply: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, fmt.Errorf("vsockexec: handshake to port %d failed: %s", port, strings.TrimSpace(line))
	}
	return conn, nil
}

// readLine reads a single '\n'-terminated line one byte at a time. A
// buffered reader would risk consuming bytes past the line -- the first
// frame the agent sends -- into a buffer this function would then discard.
func readLine(r io.Reader) (string, error) {
	var buf []byte
	b := make([]byte, 1)
	for {
		if _, err := r.Read(b); err != nil {
			return "", err
		}
		if b[0] == '\n' {
			return string(buf), nil
		}
		buf = append(buf, b[0])
		if len(buf) > maxHandshakeLine {
			return "", fmt.Errorf("vsockexec: handshake line exceeds %d bytes", maxHandshakeLine)
		}
	}
}
