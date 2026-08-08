package chapi

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func serveVMInfo(t *testing.T, sockPath, body string, status int) {
	t.Helper()
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Write([]byte(body))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

func TestVMStateRunning(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "db.sock")
	serveVMInfo(t, sock, `{"state":"Running"}`, http.StatusOK)

	state, err := VMState(sock)
	if err != nil {
		t.Fatal(err)
	}
	if state != "Running" {
		t.Errorf("VMState = %q, want %q", state, "Running")
	}
}

func TestVMStatePaused(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "db.sock")
	serveVMInfo(t, sock, `{"state":"Paused"}`, http.StatusOK)

	state, err := VMState(sock)
	if err != nil {
		t.Fatal(err)
	}
	if state != "Paused" {
		t.Errorf("VMState = %q, want %q", state, "Paused")
	}
}

func TestVMStateAbsentSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nope.sock")

	state, err := VMState(sock)
	if err != nil {
		t.Fatalf("VMState on absent socket: %v, want nil error", err)
	}
	if state != "" {
		t.Errorf("VMState = %q, want empty string for absent socket", state)
	}
}

func TestVMStateNonOKStatus(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "db.sock")
	serveVMInfo(t, sock, "", http.StatusInternalServerError)

	if _, err := VMState(sock); err == nil {
		t.Error("VMState with 500 response: want error, got nil")
	}
}

func TestVMStateConnectionRefused(t *testing.T) {
	// A socket file that exists but nothing is listening on: the exact
	// "VMM died, stale socket left behind" failure mode.
	dir := t.TempDir()
	sock := filepath.Join(dir, "db.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	// UnixListener.Close unlinks the socket file by default; disable that
	// so the stale file survives Close, matching a VMM that died without
	// cleaning up after itself.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()

	if _, err := VMState(sock); err == nil {
		t.Error("VMState against dead socket: want error, got nil")
	}
}
