package vsockexec

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

// fakeVsockListener binds a real AF_VSOCK listener on the loopback CID
// (VMADDR_CID_LOCAL) and echoes back whatever it receives on the first
// connection -- exercises DialVsock's real kernel socket()/connect() path
// without needing vhost_vsock or an actual guest (loopback vsock works
// purely in-kernel once the "vsock" module is loaded). Skips the test if
// this host has no AF_VSOCK support at all.
func fakeVsockListener(t *testing.T) (port uint32) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Skipf("AF_VSOCK not available on this host: %v", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: unix.VMADDR_PORT_ANY}); err != nil {
		unix.Close(fd)
		t.Skipf("AF_VSOCK bind failed on this host: %v", err)
	}
	sa, err := unix.Getsockname(fd)
	if err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	got, ok := sa.(*unix.SockaddrVM)
	if !ok {
		unix.Close(fd)
		t.Fatalf("Getsockname returned %T, want *unix.SockaddrVM", sa)
	}
	if err := unix.Listen(fd, 1); err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })

	go func() {
		nfd, _, err := unix.Accept(fd)
		if err != nil {
			return
		}
		defer unix.Close(nfd)
		buf := make([]byte, 64)
		n, err := unix.Read(nfd, buf)
		if err != nil || n == 0 {
			return
		}
		unix.Write(nfd, buf[:n])
	}()

	return got.Port
}

func TestDialVsockEstablishesStream(t *testing.T) {
	port := fakeVsockListener(t)

	conn, err := DialVsock(unix.VMADDR_CID_LOCAL, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

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

func TestDialVsockConnectTimeout(t *testing.T) {
	// Prove AF_VSOCK works at all on this host before trusting a
	// connection failure to mean what the test claims.
	fakeVsockListener(t)

	origTimeout, origInterval := vsockConnectTimeout, vsockConnectRetryInterval
	vsockConnectTimeout, vsockConnectRetryInterval = 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { vsockConnectTimeout, vsockConnectRetryInterval = origTimeout, origInterval })

	// Port 1 on the loopback CID: nothing listens there.
	if _, err := DialVsock(unix.VMADDR_CID_LOCAL, 1); err == nil {
		t.Error("DialVsock() to a port nothing listens on = nil error, want error")
	}
}
