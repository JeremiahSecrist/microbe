package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"microbe/internal/datadir"
	"microbe/internal/state"
)

func TestSSHArgsInteractive(t *testing.T) {
	got := sshArgs("/base/ssh/id_ed25519", "192.168.51.3", nil)
	want := []string{
		"-i", "/base/ssh/id_ed25519",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"root@192.168.51.3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sshArgs() = %v, want %v", got, want)
	}
}

func TestSSHArgsWithCommand(t *testing.T) {
	got := sshArgs("/base/ssh/id_ed25519", "192.168.51.3", []string{"cat", "/etc/hosts"})
	want := []string{
		"-i", "/base/ssh/id_ed25519",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"root@192.168.51.3",
		"--", "cat", "/etc/hosts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sshArgs() = %v, want %v", got, want)
	}
}

func writeState(t *testing.T, base string, store *state.Store) {
	t.Helper()
	if err := store.Save(filepath.Join(base, "state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestResolveGuestSSHUsesPrimaryNetworkIP(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{
		Services: map[string]state.ServiceState{
			"web": {IP: map[string]string{"backend": "192.168.51.3", "frontend": "192.168.50.3"}, Status: "running"},
		},
	})

	ip, privPath, err := resolveGuestSSH(cfgPath, "web")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.51.3" {
		t.Errorf("ip = %q, want backend (primary) IP 192.168.51.3", ip)
	}
	if privPath != filepath.Join(dataDir, "ssh", "id_ed25519") {
		t.Errorf("privPath = %q, want %q", privPath, filepath.Join(dataDir, "ssh", "id_ed25519"))
	}
}

func TestResolveGuestSSHUnknownService(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{Services: map[string]state.ServiceState{}})

	if _, _, err := resolveGuestSSH(cfgPath, "nope"); err == nil {
		t.Error("resolveGuestSSH() with unknown service = nil error, want error")
	}
}

func TestResolveGuestSSHNotRunning(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{Services: map[string]state.ServiceState{}})

	if _, _, err := resolveGuestSSH(cfgPath, "web"); err == nil {
		t.Error("resolveGuestSSH() for unstarted service = nil error, want error")
	}
}

func TestExecRunInvokesSSHWithResolvedArgsAndStdio(t *testing.T) {
	cfgPath := writeConfig(t)
	base := t.TempDir()
	datadir.Root = base
	dataDir := filepath.Join(base, "test-net")
	writeState(t, dataDir, &state.Store{
		Services: map[string]state.ServiceState{
			"db": {IP: map[string]string{"backend": "192.168.51.2"}, Status: "running"},
		},
	})

	var gotArgs []string
	origRunSSH := runSSH
	runSSH = func(args []string, stdin *os.File, stdout, stderr *os.File) error {
		gotArgs = args
		_, err := stdout.WriteString("marker")
		return err
	}
	defer func() { runSSH = origRunSSH }()

	var buf bytes.Buffer
	w, cleanup := fileFromWriter(t, &buf)
	defer cleanup()

	if err := execRun(execOptions{
		file: cfgPath, service: "db", command: []string{"echo", "hi"},
		stdin: os.Stdin, stdout: w, stderr: w,
	}); err != nil {
		t.Fatal(err)
	}

	wantArgs := sshArgs(filepath.Join(dataDir, "ssh", "id_ed25519"), "192.168.51.2", []string{"echo", "hi"})
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("runSSH args = %v, want %v", gotArgs, wantArgs)
	}
	cleanup()
	if buf.String() != "marker" {
		t.Errorf("stdout plumbed through = %q, want %q", buf.String(), "marker")
	}
}

// fileFromWriter gives execRun a real *os.File (matching ssh's Cmd.Stdout
// requirements) that copies into buf once closed, so the test can assert on
// captured output without execRun needing an io.Writer-based seam.
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
