// Package runtime launches and stops service runner processes and manages
// volume images.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"microbe/internal/cmdrun"
)

const (
	// DefaultVolumeMiB is used when a disk volume declares no size.
	DefaultVolumeMiB = 1024
	// StopGrace is how long StopService waits for SIGTERM before SIGKILL.
	StopGrace = 5 * time.Second
)

// ParseVolumeSize converts a volume size like "2G" or "512" into MiB.
// Suffixes G and M are accepted case-insensitively; a bare integer is MiB.
// An empty size yields DefaultVolumeMiB.
func ParseVolumeSize(size string) (int, error) {
	s := strings.TrimSpace(size)
	if s == "" {
		return DefaultVolumeMiB, nil
	}
	var mult float64 = 1
	last := s[len(s)-1]
	switch {
	case last == 'G' || last == 'g':
		mult = 1024
		s = s[:len(s)-1]
	case last == 'M' || last == 'm':
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("runtime: invalid volume size %q", size)
	}
	return int(math.Ceil(f * mult)), nil
}

// VolumeImagePath is the on-disk qcow2 location for a named volume.
func VolumeImagePath(base, stack, name string) string {
	return filepath.Join(base, "volumes", stack, name+".qcow2")
}

// EnsureVolume creates a qcow2 image with qemu-img only when it does not
// already exist. The command goes through r so tests can record it.
func EnsureVolume(r cmdrun.Runner, base, stack, name, size string) (string, error) {
	path := VolumeImagePath(base, stack, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	miB, err := ParseVolumeSize(size)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := r("qemu-img", "create", "-f", "qcow2", "-o", fmt.Sprintf("size=%dM", miB), path); err != nil {
		return "", err
	}
	return path, nil
}

// StartService launches a runner script as a detached background process.
// runnerPath is usually a symlink to the nix store path; it is resolved to
// the real script. The process runs with CWD runDir and appends stdout/stderr
// to logPath. Returns the child PID.
func StartService(ctx context.Context, runnerPath, runDir, logPath string) (int, error) {
	script := runnerPath
	if resolved, err := filepath.EvalSymlinks(runnerPath); err == nil {
		script = resolved
	}
	// nix build --out-link symlinks to the store DIRECTORY holding
	// bin/microvm-run; execing a directory is EACCES, so resolve it.
	if fi, err := os.Stat(script); err == nil && fi.IsDir() {
		script = filepath.Join(script, "bin", "microvm-run")
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	cmd := exec.CommandContext(ctx, script)
	cmd.Dir = runDir
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}

// StopService sends SIGTERM and, after grace, SIGKILL. A non-positive or
// already-dead PID is a no-op.
func StopService(ctx context.Context, pid int, grace time.Duration) error {
	if pid <= 0 || !processAlive(pid) {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			_ = proc.Signal(syscall.SIGKILL)
			return nil
		}
		select {
		case <-ctx.Done():
			_ = proc.Signal(syscall.SIGKILL)
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// processAlive reports whether pid is running, not a zombie. A zombie (state
// Z or X) holds its pid but is no longer executing, so it counts as dead.
func processAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return false
	}
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 1 {
		return false
	}
	state := fields[0][0]
	return state != 'Z' && state != 'X'
}
