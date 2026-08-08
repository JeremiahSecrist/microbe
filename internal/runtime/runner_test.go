package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type recorder struct {
	calls [][]string
}

func (r *recorder) run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil
}

func TestParseVolumeSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"2G", 2048, false},
		{"2g", 2048, false},
		{"512M", 512, false},
		{"512", 512, false},
		{"1.5G", 1536, false},
		{"", 1024, false},
		{"0", 0, true},
		{"abc", 0, true},
		{"-3G", 0, true},
	}
	for _, c := range cases {
		got, err := ParseVolumeSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseVolumeSize(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseVolumeSize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseVolumeSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestEnsureVolumeQemuImgCommandShape(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{}
	path, err := EnsureVolume(rec.run, dir, "db-data", "2G", "ext4")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(dir, "volumes", "db-data.qcow2")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
	want := [][]string{
		{"qemu-img", "create", "-f", "raw", "-o", "size=2048M", wantPath},
		{"mkfs.ext4", wantPath},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Errorf("calls = %v, want %v", rec.calls, want)
	}
}

func TestEnsureVolumeSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "volumes", "db-data.qcow2")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	got, err := EnsureVolume(rec.run, dir, "db-data", "2G", "ext4")
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if len(rec.calls) != 0 {
		t.Errorf("qemu-img invoked for existing volume: %v", rec.calls)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartStopService(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner", "#!/bin/sh\nsleep 30\n")
	runDir := filepath.Join(dir, "runs", "svc")
	logPath := filepath.Join(dir, "logs", "svc.log")

	pid, err := StartService(context.Background(), script, runDir, logPath)
	if err != nil {
		t.Fatal(err)
	}
	if pid <= 0 {
		t.Fatalf("StartService pid = %d, want > 0", pid)
	}
	if !processAlive(pid) {
		t.Fatalf("pid %d not alive after start", pid)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log file missing: %v", err)
	}

	if err := StopService(context.Background(), pid, time.Second); err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Error("process still alive after StopService")
	}
}

func TestStartServiceResolvesSymlink(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner-store", "#!/bin/sh\nsleep 30\n")
	link := filepath.Join(dir, "runner")
	if err := os.Symlink(script, link); err != nil {
		t.Fatal(err)
	}
	pid, err := StartService(context.Background(), link, filepath.Join(dir, "runs", "svc"), filepath.Join(dir, "logs", "svc.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer StopService(context.Background(), pid, time.Second)
	if !processAlive(pid) {
		t.Errorf("pid %d not alive via symlink", pid)
	}
}

// TestStartServiceResolvesDirLink is the red-green gate for the runner link
// shape: nix build --out-link symlinks to the store DIRECTORY (which holds
// bin/microvm-run), so StartService must resolve a directory link to its
// bin/microvm-run script instead of execing the directory (EACCES).
func TestStartServiceResolvesDirLink(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "runner-store")
	binDir := filepath.Join(storeDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, binDir, "microvm-run", "#!/bin/sh\nsleep 30\n")
	link := filepath.Join(dir, "runner")
	if err := os.Symlink(storeDir, link); err != nil {
		t.Fatal(err)
	}
	pid, err := StartService(context.Background(), link, filepath.Join(dir, "runs", "svc"), filepath.Join(dir, "logs", "svc.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer StopService(context.Background(), pid, time.Second)
	if !processAlive(pid) {
		t.Errorf("pid %d not alive via directory symlink", pid)
	}
}

// TestStartVirtiofsdResolvesDirLink proves StartVirtiofsd resolves to
// bin/virtiofsd-run (not bin/microvm-run), the same directory-symlink
// resolution StartService already exercises for microvm-run — both must
// work out of the shared startBin helper.
func TestStartVirtiofsdResolvesDirLink(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "runner-store")
	binDir := filepath.Join(storeDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, binDir, "virtiofsd-run", "#!/bin/sh\nsleep 30\n")
	link := filepath.Join(dir, "runner")
	if err := os.Symlink(storeDir, link); err != nil {
		t.Fatal(err)
	}
	pid, err := StartVirtiofsd(context.Background(), link, filepath.Join(dir, "runs", "svc"), filepath.Join(dir, "logs", "svc-virtiofsd.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer StopService(context.Background(), pid, time.Second)
	if !processAlive(pid) {
		t.Errorf("pid %d not alive via directory symlink", pid)
	}
}

func TestWaitForSocketSucceedsOnceSocketAppears(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := WaitForSocket(sockPath, 10*time.Millisecond, time.Second); err != nil {
		t.Errorf("WaitForSocket: %v", err)
	}
}

func TestWaitForSocketTimesOutWhenMissing(t *testing.T) {
	dir := t.TempDir()
	err := WaitForSocket(filepath.Join(dir, "never.sock"), 10*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Error("WaitForSocket for a socket that never appears = nil error, want timeout error")
	}
}

func TestWaitForSocketRejectsPlainFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WaitForSocket(path, 10*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Error("WaitForSocket for a plain file = nil error, want timeout error")
	}
}

func TestStopServiceEscalatesToKill(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "stubborn", "#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n")
	pid, err := StartService(context.Background(), script, filepath.Join(dir, "runs", "svc"), filepath.Join(dir, "logs", "svc.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := StopService(context.Background(), pid, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Error("process still alive after SIGKILL escalation")
	}
}

func TestStopServiceNoOpForBadPID(t *testing.T) {
	if err := StopService(context.Background(), 0, time.Second); err != nil {
		t.Errorf("StopService(0): %v", err)
	}
	if err := StopService(context.Background(), -1, time.Second); err != nil {
		t.Errorf("StopService(-1): %v", err)
	}
}
