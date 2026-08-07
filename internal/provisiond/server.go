package provisiond

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
)

// server.go implements microbe-provisiond: a root process that owns all
// privileged network state. It listens on a unix socket (created by the
// systemd socket unit, root:microbe 0660, mirroring systemd.sockets.docker)
// and applies each client request via the Ops implementation.

// Server is a provisiond listener. Serve accepts connections until the
// listener is closed or serve returns an error.
type Server struct {
	ln  net.Listener
	ops Ops
}

// NewServer returns a Server serving ops on the given listener.
func NewServer(ln net.Listener, ops Ops) *Server {
	return &Server{ln: ln, ops: ops}
}

// ListenUnix creates the unix socket at path and returns a Server backed by
// it. The socket's ownership/permissions must be set by the systemd socket
// unit; when the path does not exist the socket is created with mode 0600 and
// the caller is responsible for chowning to root:microbe 0660.
func ListenUnix(path string, ops Ops) (*Server, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("provisiond: listen %s: %w", path, err)
	}
	return NewServer(ln, ops), nil
}

// Serve accepts and handles connections until the listener is closed. The
// first connection handled may return an error; subsequent connections do not
// terminate the loop.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// Close stops the server and closes the listener.
func (s *Server) Close() error {
	return s.ln.Close()
}

// handle reads requests until the client closes (EOF), dispatching each and
// writing the response.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			writeError(conn, fmt.Errorf("provisiond: decode request: %w", err))
			return
		}
		if err := dispatch(conn, s.ops, req); err != nil {
			writeError(conn, err)
		}
	}
}

func writeError(w io.Writer, err error) {
	_ = json.NewEncoder(w).Encode(Response{Error: err.Error()})
}
