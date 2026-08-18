package portproxy

import (
	"io"
	"net"
	"sync"
)

// Serve listens on listenAddr and forwards each connection to dialAddr.
// Returns only on accept error (i.e. listener closed).
func Serve(listenAddr, dialAddr string) error {
	l, err := net.Listen("tcp6", listenAddr)
	if err != nil {
		return err
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return nil
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
		out.(*net.TCPConn).CloseWrite()
	}()
	go func() {
		defer wg.Done()
		io.Copy(in, out)
		in.(*net.TCPConn).CloseWrite()
	}()
	wg.Wait()
	in.Close()
	out.Close()
}
