package cmd

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"microbe/internal/datadir"
	"microbe/internal/state"
	"microbe/internal/vsockexec"
)

func writeState(t *testing.T, base string, store *state.Store) {
	t.Helper()
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestResolveGuestVsockRunning(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{
		Services: map[string]state.ServiceState{
			"web": {Status: "running"},
		},
	})

	got, err := resolveGuestVsock(cfgPath, "web")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "runs", "web", "notify.vsock")
	if got != want {
		t.Errorf("resolveGuestVsock() = %q, want %q", got, want)
	}
}

func TestResolveGuestVsockUnknownService(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{Services: map[string]state.ServiceState{}})

	if _, err := resolveGuestVsock(cfgPath, "nope"); err == nil {
		t.Error("resolveGuestVsock() with unknown service = nil error, want error")
	}
}

func TestResolveGuestVsockNotRunning(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{Services: map[string]state.ServiceState{}})

	if _, err := resolveGuestVsock(cfgPath, "web"); err == nil {
		t.Error("resolveGuestVsock() for unstarted service = nil error, want error")
	}
}

// fakeAgent stands in for the guest agent on the other end of dialAgent:
// the test reads the header the host sent off agentConn and writes back
// whatever response frames it likes.
type fakeAgent struct {
	hostConn, agentConn net.Conn
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	host, agent := net.Pipe()
	t.Cleanup(func() { host.Close(); agent.Close() })
	return &fakeAgent{hostConn: host, agentConn: agent}
}

func withFakeAgent(t *testing.T, fa *fakeAgent) {
	t.Helper()
	orig := dialAgent
	dialAgent = func(string) (io.ReadWriteCloser, error) { return fa.hostConn, nil }
	t.Cleanup(func() { dialAgent = orig })
}

// fileFromReader gives a test a real *os.File for execOptions.stdin,
// backed by r.
func fileFromReader(t *testing.T, r io.Reader) *os.File {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		io.Copy(pw, r)
		pw.Close()
	}()
	t.Cleanup(func() { pr.Close() })
	return pr
}

// fileFromWriter gives a test a real *os.File (matching runAgentSession's
// *os.File stdout/stderr) that copies into buf once closed.
func fileFromWriter(t *testing.T, buf *bytes.Buffer) (*os.File, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		buf.ReadFrom(r)
		close(done)
	}()
	return w, func() {
		w.Close()
		<-done
	}
}

func TestRunAgentSessionSendsHeaderAndPlumbsOutput(t *testing.T) {
	fa := newFakeAgent(t)
	withFakeAgent(t, fa)

	headerCh := make(chan vsockexec.Header, 1)
	go func() {
		h, err := vsockexec.ReadHeader(fa.agentConn)
		if err != nil {
			close(headerCh)
			return
		}
		// Drain further frames (the stdin-EOF frame runAgentSession's
		// pumpStdin sends): net.Pipe is unbuffered, so that write would
		// otherwise block forever with nothing on this end reading it.
		go io.Copy(io.Discard, fa.agentConn)
		vsockexec.WriteFrame(fa.agentConn, vsockexec.FrameStdout, []byte("out"))
		vsockexec.WriteFrame(fa.agentConn, vsockexec.FrameStderr, []byte("err"))
		vsockexec.WriteExit(fa.agentConn, 0)
		headerCh <- h
	}()

	var outBuf, errBuf bytes.Buffer
	outW, outCleanup := fileFromWriter(t, &outBuf)
	errW, errCleanup := fileFromWriter(t, &errBuf)

	err := runAgentSession("unused", []string{"echo", "hi"}, false, fileFromReader(t, bytes.NewReader(nil)), outW, errW)
	outCleanup()
	errCleanup()
	if err != nil {
		t.Fatalf("runAgentSession() error = %v", err)
	}

	h, ok := <-headerCh
	if !ok {
		t.Fatal("agent never received a header")
	}
	if h.TTY {
		t.Error("header.TTY = true, want false for a non-interactive session")
	}
	if len(h.Argv) != 2 || h.Argv[0] != "echo" || h.Argv[1] != "hi" {
		t.Errorf("header.Argv = %v, want [echo hi]", h.Argv)
	}
	if outBuf.String() != "out" {
		t.Errorf("stdout = %q, want %q", outBuf.String(), "out")
	}
	if errBuf.String() != "err" {
		t.Errorf("stderr = %q, want %q", errBuf.String(), "err")
	}
}

func TestRunAgentSessionNonzeroExitIsError(t *testing.T) {
	fa := newFakeAgent(t)
	withFakeAgent(t, fa)

	go func() {
		vsockexec.ReadHeader(fa.agentConn)
		go io.Copy(io.Discard, fa.agentConn)
		vsockexec.WriteExit(fa.agentConn, 7)
	}()

	var outBuf, errBuf bytes.Buffer
	outW, outCleanup := fileFromWriter(t, &outBuf)
	errW, errCleanup := fileFromWriter(t, &errBuf)
	defer outCleanup()
	defer errCleanup()

	err := runAgentSession("unused", []string{"false"}, false, fileFromReader(t, bytes.NewReader(nil)), outW, errW)
	if err == nil {
		t.Error("runAgentSession() with nonzero guest exit = nil error, want error")
	}
}

func TestExecRunUsesNonInteractiveHeader(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{
		Services: map[string]state.ServiceState{"db": {Status: "running"}},
	})

	fa := newFakeAgent(t)
	withFakeAgent(t, fa)

	headerCh := make(chan vsockexec.Header, 1)
	go func() {
		h, _ := vsockexec.ReadHeader(fa.agentConn)
		go io.Copy(io.Discard, fa.agentConn)
		vsockexec.WriteExit(fa.agentConn, 0)
		headerCh <- h
	}()

	var outBuf, errBuf bytes.Buffer
	outW, outCleanup := fileFromWriter(t, &outBuf)
	errW, errCleanup := fileFromWriter(t, &errBuf)

	if err := execRun(execOptions{
		file: cfgPath, service: "db", command: []string{"cat", "/etc/hosts"},
		stdin: fileFromReader(t, bytes.NewReader(nil)), stdout: outW, stderr: errW,
	}); err != nil {
		t.Fatal(err)
	}
	outCleanup()
	errCleanup()

	h := <-headerCh
	if h.TTY {
		t.Error("execRun's header.TTY = true, want false")
	}
	if len(h.Argv) != 2 || h.Argv[0] != "cat" || h.Argv[1] != "/etc/hosts" {
		t.Errorf("header.Argv = %v, want [cat /etc/hosts]", h.Argv)
	}
}

func TestShellRunUsesTTYHeaderAndDefaultArgv(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{
		Services: map[string]state.ServiceState{"db": {Status: "running"}},
	})

	fa := newFakeAgent(t)
	withFakeAgent(t, fa)

	headerCh := make(chan vsockexec.Header, 1)
	go func() {
		h, _ := vsockexec.ReadHeader(fa.agentConn)
		go io.Copy(io.Discard, fa.agentConn)
		vsockexec.WriteExit(fa.agentConn, 0)
		headerCh <- h
	}()

	var outBuf, errBuf bytes.Buffer
	outW, outCleanup := fileFromWriter(t, &outBuf)
	errW, errCleanup := fileFromWriter(t, &errBuf)

	if err := shellRun(execOptions{
		file: cfgPath, service: "db",
		stdin: fileFromReader(t, bytes.NewReader(nil)), stdout: outW, stderr: errW,
	}); err != nil {
		t.Fatal(err)
	}
	outCleanup()
	errCleanup()

	h := <-headerCh
	if !h.TTY {
		t.Error("shellRun's header.TTY = false, want true")
	}
	if len(h.Argv) == 0 {
		t.Fatal("shellRun's header.Argv is empty, want defaultShellArgv")
	}
}
