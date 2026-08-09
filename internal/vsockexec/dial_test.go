package vsockexec

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
)

// fakeHybridVsockProxy stands in for cloud-hypervisor's own UDS: it accepts
// one connection, reads the "CONNECT <port>\n" line, replies with reply,
// then (if okAfterReply) echoes whatever it receives back verbatim -- this
// is what a real guest-side agent connection looks like once the handshake
// succeeds.
func fakeHybridVsockProxy(t *testing.T, reply string, okAfterReply bool) (udsPath string, gotPort chan string) {
	t.Helper()
	udsPath = filepath.Join(t.TempDir(), "notify.vsock")
	ln, err := net.Listen("unix", udsPath)
	if err != nil {
		t.Fatal(err)
	}
	gotPort = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return
		}
		var port string
		fmt.Sscanf(line, "CONNECT %s", &port)
		gotPort <- port
		io.WriteString(conn, reply)
		if okAfterReply {
			io.Copy(conn, conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return udsPath, gotPort
}

func TestDialHybridVsockSendsConnectAndEstablishesStream(t *testing.T) {
	udsPath, gotPort := fakeHybridVsockProxy(t, "OK 1073741824\n", true)

	conn, err := DialHybridVsock(udsPath, 6969)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if port := <-gotPort; port != "6969" {
		t.Errorf("proxy saw CONNECT port %q, want \"6969\"", port)
	}

	msg := []byte("round trip\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(msg) {
		t.Errorf("echoed = %q, want %q", buf, msg)
	}
}

func TestDialHybridVsockRejectsNonOKReply(t *testing.T) {
	udsPath, _ := fakeHybridVsockProxy(t, "ERROR no such port\n", false)

	if _, err := DialHybridVsock(udsPath, 6969); err == nil {
		t.Error("DialHybridVsock() with a non-OK reply = nil error, want error")
	}
}

func TestDialHybridVsockMissingSocket(t *testing.T) {
	if _, err := DialHybridVsock(filepath.Join(t.TempDir(), "does-not-exist.vsock"), 6969); err == nil {
		t.Error("DialHybridVsock() against a missing socket = nil error, want error")
	}
}
