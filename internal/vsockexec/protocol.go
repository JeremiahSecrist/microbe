// Package vsockexec implements microbe's guest-exec transport: a small
// framed protocol carried over a vsock connection to the per-service guest
// agent, replacing SSH so exec/shell never depend on network reachability
// or a key-authorized sshd running in every guest (see internal/cmd's
// exec.go/shell.go). The host side of this package is importable and unit
// tested here; the guest agent is a separate, dependency-free Go program
// (see internal/nix/flakegen/agent) that speaks the identical wire format
// without importing this package, so any change here must be mirrored
// there by hand.
package vsockexec

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// AgentPort is the fixed vsock port every guest's microbe-agent listens on.
// The guest agent (internal/nix/flakegen/agent/main.go, a separate
// dependency-free Go program the Nix side builds and runs, not something
// that can import this package) hardcodes the identical value -- keep the
// two in sync if this ever changes.
const AgentPort uint32 = 6969

// Frame types. Header/Resize flow host->agent only; Stdin flows host->agent
// with a zero-length frame signaling stdin EOF; Stdout/Stderr/Exit flow
// agent->host only. Stderr is unused in TTY mode (the pty already merges
// the child's stderr into its single output stream).
const (
	FrameHeader byte = 1
	FrameStdin  byte = 2
	FrameStdout byte = 3
	FrameStderr byte = 4
	FrameResize byte = 5
	FrameExit   byte = 6
)

// maxFrameLen bounds a frame's declared payload length: a guard against a
// corrupt or hostile length prefix causing an unbounded allocation, well
// above anything a single stdio chunk or JSON header should ever need.
const maxFrameLen = 1 << 20

// Header is the first frame's JSON payload: the command to run and whether
// the agent should allocate a pty for it.
type Header struct {
	Argv []string `json:"argv"`
	TTY  bool      `json:"tty"`
	Cols uint16    `json:"cols,omitempty"`
	Rows uint16    `json:"rows,omitempty"`
}

// WriteFrame writes one frame: a 1-byte type, a 4-byte big-endian payload
// length, then the payload itself.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one frame written by WriteFrame.
func ReadFrame(r io.Reader) (typ byte, payload []byte, err error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrameLen {
		return 0, nil, fmt.Errorf("vsockexec: frame length %d exceeds max %d", n, maxFrameLen)
	}
	payload = make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return hdr[0], payload, nil
}

// WriteHeader JSON-encodes h into a FrameHeader frame.
func WriteHeader(w io.Writer, h Header) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return WriteFrame(w, FrameHeader, b)
}

// ReadHeader reads and decodes a FrameHeader frame.
func ReadHeader(r io.Reader) (Header, error) {
	typ, payload, err := ReadFrame(r)
	if err != nil {
		return Header{}, err
	}
	if typ != FrameHeader {
		return Header{}, fmt.Errorf("vsockexec: expected header frame (type %d), got type %d", FrameHeader, typ)
	}
	var h Header
	if err := json.Unmarshal(payload, &h); err != nil {
		return Header{}, err
	}
	return h, nil
}

// EncodeResize builds a FrameResize frame's payload: rows then cols, each
// a big-endian uint16. Exported (rather than folded into WriteResize) so a
// caller that needs to serialize its own frame writes -- e.g. the host CLI
// interleaving stdin and resize frames under one lock -- can build the
// payload without a second entry point into WriteFrame.
func EncodeResize(rows, cols uint16) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], rows)
	binary.BigEndian.PutUint16(payload[2:4], cols)
	return payload
}

// WriteResize writes a FrameResize frame.
func WriteResize(w io.Writer, rows, cols uint16) error {
	return WriteFrame(w, FrameResize, EncodeResize(rows, cols))
}

// ReadResize decodes a FrameResize frame's payload (as returned by ReadFrame).
func ReadResize(payload []byte) (rows, cols uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, fmt.Errorf("vsockexec: resize payload length = %d, want 4", len(payload))
	}
	return binary.BigEndian.Uint16(payload[0:2]), binary.BigEndian.Uint16(payload[2:4]), nil
}

// WriteExit writes a FrameExit frame carrying code as a big-endian int32.
// The agent closes the connection immediately after sending this.
func WriteExit(w io.Writer, code int32) error {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(code))
	return WriteFrame(w, FrameExit, payload)
}

// ReadExit decodes a FrameExit frame's payload (as returned by ReadFrame).
func ReadExit(payload []byte) (int32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("vsockexec: exit payload length = %d, want 4", len(payload))
	}
	return int32(binary.BigEndian.Uint32(payload)), nil
}
