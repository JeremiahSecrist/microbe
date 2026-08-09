package cmd

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/nix/flakegen"
	"microbe/internal/state"
	"microbe/internal/vsockexec"
)

// guestAddr is where to reach svc's agent: exactly one of UDSPath (nixos,
// cloud-hypervisor's hybrid-vsock UDS) or CID (finix, real AF_VSOCK) is
// meaningful, selected by OS -- the two guest types have no shared
// transport, see dialAgent.
type guestAddr struct {
	OS      string // "nixos" or "finix"
	UDSPath string
	CID     uint32
}

// dialAgent is a seam so tests can fake the vsock connection without a real
// guest; production dispatches on addr.OS to the matching real transport.
var dialAgent = func(addr guestAddr) (io.ReadWriteCloser, error) {
	if addr.OS == "finix" {
		return vsockexec.DialVsock(addr.CID, vsockexec.AgentPort)
	}
	return vsockexec.DialHybridVsock(addr.UDSPath, vsockexec.AgentPort)
}

// defaultShellArgv is what `microbe shell` runs when given no explicit
// command: a login shell, the same "interactive session with no command"
// meaning ssh gave `microbe shell` before.
var defaultShellArgv = []string{"/bin/sh", "-l"}

// resolveGuestVsock loads the compose config (to confirm svc exists and
// find its OS) and the running state (to confirm svc is up), then returns
// where to reach svc's agent -- a notify.vsock UNIX socket path for nixos
// guests (cloud-hypervisor's own hybrid-vsock device, see up.go's runDir
// convention, base/runs/<svc>), or a CID for finix guests (real AF_VSOCK,
// looked up from generated.json since it's assigned once at render time
// and otherwise only known to the rendered Nix -- see
// flakegen.LoadGeneratedCID and finix-agent.nix, which reads the same
// file to wire the matching QEMU vsock device). Unlike SSH this needs no
// IP, no network attachment, and no keys -- reachability is a local
// filesystem permission (nixos) or a kernel-assigned CID (finix), not a
// network path.
func resolveGuestVsock(file, svc string) (guestAddr, error) {
	cfg, err := config.Load(file)
	if err != nil {
		return guestAddr{}, err
	}
	svcCfg, ok := cfg.Services[svc]
	if !ok {
		return guestAddr{}, fmt.Errorf("no service %q", svc)
	}
	base := datadir.Dir(cfg.Name)
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		return guestAddr{}, err
	}
	if _, ok := store.Services[svc]; !ok {
		return guestAddr{}, fmt.Errorf("service %q is not running", svc)
	}
	if svcCfg.OS == "finix" {
		cid, err := flakegen.LoadGeneratedCID(filepath.Dir(file), svc)
		if err != nil {
			return guestAddr{}, err
		}
		return guestAddr{OS: "finix", CID: uint32(cid)}, nil
	}
	return guestAddr{OS: "nixos", UDSPath: filepath.Join(base, "runs", svc, "notify.vsock")}, nil
}

// frameWriter serializes frame writes to the agent connection: stdin
// forwarding and (in tty mode) resize forwarding run on separate
// goroutines, and a frame's header+payload must land on the wire as one
// contiguous unit.
type frameWriter struct {
	mu   sync.Mutex
	conn io.Writer
}

func (f *frameWriter) writeFrame(typ byte, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return vsockexec.WriteFrame(f.conn, typ, payload)
}

// pumpStdin forwards stdin to the agent as frames until stdin errors/EOFs,
// then sends the zero-length stdin frame that signals EOF to the guest
// command.
func pumpStdin(fw *frameWriter, stdin io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			fw.writeFrame(vsockexec.FrameStdin, buf[:n])
		}
		if err != nil {
			fw.writeFrame(vsockexec.FrameStdin, nil)
			return
		}
	}
}

// watchTTY puts stdin into raw mode and forwards host terminal resizes to
// the guest for the life of an interactive session, when stdin/stdout are
// real terminals -- a redirected/piped session has no terminal to make raw
// or resize. Returns a cleanup func restoring terminal state; nil if there
// was nothing to set up.
func watchTTY(fw *frameWriter, stdin, stdout *os.File) func() {
	if !isTerminal(stdin) || !isTerminal(stdout) {
		return nil
	}
	fd := int(stdin.Fd())
	old, err := makeRaw(fd)
	if err != nil {
		return nil
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				if rows, cols, err := termSize(int(stdout.Fd())); err == nil {
					fw.writeFrame(vsockexec.FrameResize, vsockexec.EncodeResize(rows, cols))
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		close(done)
		signal.Stop(sig)
		restoreTerm(fd, old)
	}
}

// runAgentSession dials svc's agent at addr, sends the command header,
// then pumps stdin/stdout/stderr as frames until the guest sends an exit
// frame. A nonzero guest exit code surfaces as an error, matching the
// prior ssh-based exec's behavior (an *exec.ExitError also just became a
// generic error).
func runAgentSession(addr guestAddr, command []string, tty bool, stdin, stdout, stderr *os.File) error {
	conn, err := dialAgent(addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	argv := command
	if len(argv) == 0 {
		argv = defaultShellArgv
	}
	h := vsockexec.Header{Argv: argv, TTY: tty}
	if tty {
		if rows, cols, err := termSize(int(stdout.Fd())); err == nil {
			h.Rows, h.Cols = rows, cols
		}
	}
	if err := vsockexec.WriteHeader(conn, h); err != nil {
		return fmt.Errorf("vsock: send command: %w", err)
	}

	fw := &frameWriter{conn: conn}

	if tty {
		if restore := watchTTY(fw, stdin, stdout); restore != nil {
			defer restore()
		}
	}
	go pumpStdin(fw, stdin)

	for {
		typ, payload, err := vsockexec.ReadFrame(conn)
		if err != nil {
			return fmt.Errorf("vsock: session ended unexpectedly: %w", err)
		}
		switch typ {
		case vsockexec.FrameStdout:
			stdout.Write(payload)
		case vsockexec.FrameStderr:
			stderr.Write(payload)
		case vsockexec.FrameExit:
			code, err := vsockexec.ReadExit(payload)
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("exit status %d", code)
			}
			return nil
		}
	}
}
