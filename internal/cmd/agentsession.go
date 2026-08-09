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
	"microbe/internal/state"
	"microbe/internal/vsockexec"
)

// dialAgent is a seam so tests can fake the vsock connection without a real
// guest; production always goes through vsockexec.DialHybridVsock.
var dialAgent = func(udsPath string) (io.ReadWriteCloser, error) {
	return vsockexec.DialHybridVsock(udsPath, vsockexec.AgentPort)
}

// defaultShellArgv is what `microbe shell` runs when given no explicit
// command: a login shell, the same "interactive session with no command"
// meaning ssh gave `microbe shell` before.
var defaultShellArgv = []string{"/bin/sh", "-l"}

// resolveGuestVsock loads the compose config (to confirm svc exists and to
// find the stack's data dir) and the running state (to confirm svc is up)
// to find the notify.vsock cloud-hypervisor exposes for svc: the
// CLI-owned, permission-gated UNIX socket that bridges the host to the
// guest's own vsock device (see internal/vsockexec, and up.go's runDir
// convention, base/runs/<svc>). Unlike SSH this needs no IP, no network
// attachment, and no keys -- reachability is a local filesystem
// permission, not a network path.
func resolveGuestVsock(file, svc string) (string, error) {
	cfg, err := config.Load(file)
	if err != nil {
		return "", err
	}
	if _, ok := cfg.Services[svc]; !ok {
		return "", fmt.Errorf("no service %q", svc)
	}
	base := datadir.Dir(cfg.Name)
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		return "", err
	}
	if _, ok := store.Services[svc]; !ok {
		return "", fmt.Errorf("service %q is not running", svc)
	}
	return filepath.Join(base, "runs", svc, "notify.vsock"), nil
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

// runAgentSession dials svc's agent at udsPath, sends the command header,
// then pumps stdin/stdout/stderr as frames until the guest sends an exit
// frame. A nonzero guest exit code surfaces as an error, matching the
// prior ssh-based exec's behavior (an *exec.ExitError also just became a
// generic error).
func runAgentSession(udsPath string, command []string, tty bool, stdin, stdout, stderr *os.File) error {
	conn, err := dialAgent(udsPath)
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
