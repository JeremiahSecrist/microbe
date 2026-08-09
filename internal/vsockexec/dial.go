package vsockexec

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// maxHandshakeLine bounds the hybrid-vsock handshake reply: a guard
// against a misbehaving peer never sending '\n', not a real protocol limit.
const maxHandshakeLine = 256

// vsockConnectTimeout/vsockConnectRetryInterval bound DialVsock's connect
// retry loop -- vars, not consts, so tests can shrink them. The guest's
// agent takes a few seconds after boot before finit has it listening, and
// unlike DialHybridVsock's UDS (created as a side effect of the
// hypervisor's own startup) there's no socket file to poll for existence
// first, so the retry has to be on the connect call itself. 10s/200ms
// mirrors the retry cadence finix-base.nix already uses for its own
// boot-time mount retry.
var (
	vsockConnectTimeout       = 10 * time.Second
	vsockConnectRetryInterval = 200 * time.Millisecond
)

// DialVsock reaches the guest agent listening on vsock port through real
// kernel AF_VSOCK (vhost-vsock-pci), used for finix guests -- unlike
// DialHybridVsock, this dials the guest's CID directly: no UDS, no
// CONNECT/OK handshake. Requires the vhost_vsock kernel module loaded and
// /dev/vhost-vsock accessible to the caller.
//
// Returns *os.File, not net.Conn: Go's net.FileConn rejects AF_VSOCK
// sockets outright (the net package doesn't recognize the address
// family), same reason the guest-side agent (agent/main.go) hands its
// accepted connections around as *os.File rather than net.Conn.
func DialVsock(cid, port uint32) (*os.File, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsockexec: socket: %w", err)
	}

	sa := &unix.SockaddrVM{CID: cid, Port: port}
	deadline := time.Now().Add(vsockConnectTimeout)
	for {
		err = unix.Connect(fd, sa)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			unix.Close(fd)
			return nil, fmt.Errorf("vsockexec: connect cid %d port %d: %w", cid, port, err)
		}
		time.Sleep(vsockConnectRetryInterval)
	}

	return os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d:%d", cid, port)), nil
}

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
