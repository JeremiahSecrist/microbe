// Command microbe-agent is microbe's guest-side exec/shell backend: it
// listens on a fixed AF_VSOCK port and runs whatever command the host asks
// for, replacing SSH so exec/shell need no guest network reachability, no
// sshd, and no injected keypair. The host reaches this agent through
// cloud-hypervisor's own vsock device (see internal/vsockexec), which is
// itself only reachable via a host-local, permission-gated UNIX socket --
// that filesystem permission is the whole access-control boundary, the
// same trust model microbe already uses for the cloud-hypervisor API
// socket and virtiofsd sockets, so the agent itself does no auth of its
// own.
//
// This program is intentionally dependency-free (stdlib only): it's built
// per-project by Nix (see ../parts/agent.nix) from source written verbatim
// into the project directory by WriteStack, so it must build offline with
// no module fetching. That also means it can't import internal/vsockexec
// -- the wire protocol below is a hand-kept mirror of
// internal/vsockexec/protocol.go. Keep the two in sync by hand if either
// changes.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// agentPort must match internal/vsockexec.AgentPort.
const agentPort = 6969

// --- wire protocol (mirrors internal/vsockexec/protocol.go) ---

const (
	frameHeader byte = 1
	frameStdin  byte = 2
	frameStdout byte = 3
	frameStderr byte = 4
	frameResize byte = 5
	frameExit   byte = 6
)

type header struct {
	Argv []string `json:"argv"`
	TTY  bool     `json:"tty"`
	Cols uint16   `json:"cols,omitempty"`
	Rows uint16   `json:"rows,omitempty"`
}

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	hdr := [5]byte{typ, byte(len(payload) >> 24), byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(hdr[1])<<24 | int(hdr[2])<<16 | int(hdr[3])<<8 | int(hdr[4])
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return hdr[0], payload, nil
}

func decodeResize(p []byte) (rows, cols uint16, err error) {
	if len(p) != 4 {
		return 0, 0, fmt.Errorf("bad resize payload length %d", len(p))
	}
	return uint16(p[0])<<8 | uint16(p[1]), uint16(p[2])<<8 | uint16(p[3]), nil
}

func exitPayload(code int32) []byte {
	return []byte{byte(code >> 24), byte(code >> 16), byte(code >> 8), byte(code)}
}

// --- AF_VSOCK ---
//
// net.FileListener/FileConn reject AF_VSOCK sockets (Go's net package
// doesn't recognize the address family), and the standard library's
// syscall.Sockaddr interface has no exported way to add a new family from
// outside the package. So socket/bind/listen/accept go straight through
// syscall.Syscall with a hand-built sockaddr_vm, matching struct
// sockaddr_vm from linux/vm_sockets.h byte for byte.

type sockaddrVM struct {
	family   uint16
	reserved uint16
	port     uint32
	cid      uint32
	flags    uint8
	zero     [3]uint8
}

const (
	afVSock      = 40
	vmAddrCIDAny = 0xffffffff
)

func vsockListen(port uint32) (int, error) {
	fd, _, errno := syscall.Syscall(syscall.SYS_SOCKET, afVSock, syscall.SOCK_STREAM, 0)
	if errno != 0 {
		return 0, errno
	}
	sa := sockaddrVM{family: afVSock, port: port, cid: vmAddrCIDAny}
	if _, _, errno := syscall.Syscall(syscall.SYS_BIND, fd, uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); errno != 0 {
		syscall.Close(int(fd))
		return 0, errno
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_LISTEN, fd, 16, 0); errno != 0 {
		syscall.Close(int(fd))
		return 0, errno
	}
	return int(fd), nil
}

func vsockAccept(lfd int) (*os.File, error) {
	nfd, _, errno := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(lfd), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	return os.NewFile(nfd, "vsock-conn"), nil
}

// --- pty allocation ---
//
// No external pty package (dependency-free build constraint), so this
// hand-rolls the standard /dev/ptmx dance: unlock the slave, read its
// number back, open /dev/pts/<n>. exec.Cmd's own SysProcAttr (Setsid +
// Setctty) handles making the slave the child's controlling terminal --
// that part needs no extra syscalls.

const (
	tiocgptn   = 0x80045430
	tiocsptlck = 0x40045431
	tiocswinsz = 0x5414
)

func ioctl(fd uintptr, req uintptr, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if err := ioctl(m.Fd(), tiocsptlck, uintptr(unsafe.Pointer(&unlock))); err != nil {
		m.Close()
		return nil, nil, err
	}
	var n int32
	if err := ioctl(m.Fd(), tiocgptn, uintptr(unsafe.Pointer(&n))); err != nil {
		m.Close()
		return nil, nil, err
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

type winsize struct {
	rows, cols, xpixel, ypixel uint16
}

func setWinsize(f *os.File, rows, cols uint16) {
	ws := winsize{rows: rows, cols: cols}
	ioctl(f.Fd(), tiocswinsz, uintptr(unsafe.Pointer(&ws)))
}

// --- session handling ---

// frameWriter serializes frame writes: the output pump(s) and the final
// exit frame all write from different goroutines, and a frame's
// header+payload must land on the wire as one contiguous unit.
type frameWriter struct {
	mu   sync.Mutex
	conn io.Writer
}

func (f *frameWriter) write(typ byte, payload []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeFrame(f.conn, typ, payload)
}

// frameStream adapts a frameWriter into an io.Writer for exec.Cmd's
// Stdout/Stderr: each Write becomes one frame of typ.
type frameStream struct {
	fw  *frameWriter
	typ byte
}

func (s *frameStream) Write(p []byte) (int, error) {
	s.fw.write(s.typ, p)
	return len(p), nil
}

// pumpInput reads frames from conn until it errors (conn closed) or
// blocks forever trying to hand data to a stdinW no one drains -- bounded
// by the connection/process lifetime either way. stdinW receives raw
// stdin bytes; onEOF (nil for a tty, since closing the pty master would
// kill the whole session) fires on a zero-length stdin frame; resize (nil
// outside tty mode) fires on a resize frame.
func pumpInput(conn io.Reader, stdinW io.Writer, onEOF func(), resize func(rows, cols uint16)) {
	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case frameStdin:
			if len(payload) == 0 {
				if onEOF != nil {
					onEOF()
				}
				continue
			}
			if stdinW != nil {
				stdinW.Write(payload)
			}
		case frameResize:
			if resize != nil {
				if rows, cols, err := decodeResize(payload); err == nil {
					resize(rows, cols)
				}
			}
		}
	}
}

func runTTY(conn *os.File, fw *frameWriter, h header) {
	master, slave, err := openPTY()
	if err != nil {
		fw.write(frameStderr, []byte("microbe-agent: open pty: "+err.Error()+"\n"))
		fw.write(frameExit, exitPayload(1))
		return
	}
	if h.Cols > 0 || h.Rows > 0 {
		setWinsize(master, h.Rows, h.Cols)
	}

	cmd := exec.Command(h.Argv[0], h.Argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	if err := cmd.Start(); err != nil {
		fw.write(frameStderr, []byte(err.Error()+"\n"))
		fw.write(frameExit, exitPayload(1))
		slave.Close()
		master.Close()
		return
	}
	slave.Close()

	go pumpInput(conn, master, nil, func(rows, cols uint16) { setWinsize(master, rows, cols) })

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				fw.write(frameStdout, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	cmd.Wait()
	master.Close()
	<-outDone
	fw.write(frameExit, exitPayload(int32(cmd.ProcessState.ExitCode())))
}

func runPipe(conn *os.File, fw *frameWriter, h header) {
	pipeR, pipeW := io.Pipe()
	cmd := exec.Command(h.Argv[0], h.Argv[1:]...)
	cmd.Stdin = pipeR
	cmd.Stdout = &frameStream{fw: fw, typ: frameStdout}
	cmd.Stderr = &frameStream{fw: fw, typ: frameStderr}

	if err := cmd.Start(); err != nil {
		fw.write(frameStderr, []byte(err.Error()+"\n"))
		fw.write(frameExit, exitPayload(1))
		return
	}

	go pumpInput(conn, pipeW, func() { pipeW.Close() }, nil)

	cmd.Wait()
	fw.write(frameExit, exitPayload(int32(cmd.ProcessState.ExitCode())))
}

func handleConn(conn *os.File) {
	defer conn.Close()

	typ, payload, err := readFrame(conn)
	if err != nil || typ != frameHeader {
		return
	}
	var h header
	if err := json.Unmarshal(payload, &h); err != nil || len(h.Argv) == 0 {
		return
	}

	fw := &frameWriter{conn: conn}
	if h.TTY {
		runTTY(conn, fw, h)
	} else {
		runPipe(conn, fw, h)
	}
}

func main() {
	lfd, err := vsockListen(agentPort)
	if err != nil {
		fmt.Fprintln(os.Stderr, "microbe-agent: listen:", err)
		os.Exit(1)
	}
	for {
		conn, err := vsockAccept(lfd)
		if err != nil {
			fmt.Fprintln(os.Stderr, "microbe-agent: accept:", err)
			continue
		}
		go handleConn(conn)
	}
}
