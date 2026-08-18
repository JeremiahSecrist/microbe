package portproxy

import (
	"io"
	"net"
	"sync"
)

// halfCloser is implemented by net.TCPConn to shut down one direction of a
// TCP stream, so the far side sees EOF without waiting for the local side to
// close the fd entirely.
type halfCloser interface {
	CloseWrite() error
}

// Serve listens on listenAddr and forwards each connection to dialAddr.
// Runs forever: accept errors (interrupts, transient fd pressure) are
// retried rather than killing the forwarder. Returns only when the listener
// is closed.
func Serve(listenAddr, dialAddr string) error {
	l, err := net.Listen("tcp6", listenAddr)
	if err != nil {
		return err
	}
	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go forward(c, dialAddr)
	}
}

func forward(in net.Conn, addr string) {
	out, err := net.Dial("tcp6", addr)
	if err != nil {
		in.Close()
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(out, in)
		closeWrite(out)
	}()
	go func() {
		defer wg.Done()
		io.Copy(in, out)
		closeWrite(in)
	}()
	wg.Wait()
	in.Close()
	out.Close()
}

func closeWrite(c net.Conn) {
	if hc, ok := c.(halfCloser); ok {
		_ = hc.CloseWrite()
		return
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}
