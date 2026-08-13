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

// VolumeImagePath is the on-disk volume location for a named volume. The
// path keeps a ".qcow2" suffix per the CLI-managed volume dir contract, but
// the image is raw (see EnsureVolume) so mkfs can format it without root.
// base is already stack-scoped (see internal/datadir), so no separate stack
// subdirectory is needed here.
func VolumeImagePath(base, name string) string {
	return filepath.Join(base, "volumes", name+".qcow2")
}

// EnsureVolume creates a raw image with qemu-img and formats it with mkfs
// only when it does not already exist. Raw, not qcow2: mkfs writes a
// filesystem directly onto the file's bytes, which only works unprivileged
// for a raw layout — a qcow2 container needs qemu-nbd (root) to expose it as
// a block device first. The renderer declares imageType = "raw" to match.
// The command goes through run so tests can record it.
func EnsureVolume(run cmdrun.Runner, base, name, size, fsType string) (string, error) {
	path := VolumeImagePath(base, name)
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
	if err := run("qemu-img", "create", "-f", "raw", "-o", fmt.Sprintf("size=%dM", miB), path); err != nil {
		return "", err
	}
	if err := run("mkfs."+fsType, path); err != nil {
		return "", err
	}
	return path, nil
}

// StartService launches the runner's bin/microvm-run as a detached
// background process. See startBin.
func StartService(ctx context.Context, runnerPath, runDir, logPath string) (int, error) {
	return startBin(ctx, runnerPath, "microvm-run", runDir, logPath, nil)
}

// StartVirtiofsd launches the runner's bin/virtiofsd-run as a detached
// background process. It only exists in the runner's store output when the
// service declares at least one virtiofs share (microvm.nix's
// microvm.binScripts.virtiofsd-run, gated by requiresVirtiofsd); one
// process serves all of a service's virtiofs shares via supervisord. Must
// be started (and its socket(s) ready, see WaitForSocket) before the VM
// itself, since cloud-hypervisor connects to the virtiofsd socket at boot
// with no documented retry. Reuses the same runnerPath/runDir as
// StartService so the shares' relative socket filenames
// (config.microvm.shares[].socket, CWD-relative) resolve identically for
// both processes. env is appended to the inherited environment; nil means
// inherit only.
func StartVirtiofsd(ctx context.Context, runnerPath, runDir, logPath string, env []string) (int, error) {
	return startBin(ctx, runnerPath, "virtiofsd-run", runDir, logPath, env)
}

// startBin launches runnerPath's bin/<binName> as a detached background
// process. runnerPath is usually a symlink to the nix store path; it is
// resolved to the real script. The process runs with CWD runDir and
// appends stdout/stderr to logPath. env, when non-nil, is set as the
// child's environment (use os.Environ() + extra vars to inherit + extend).
// Returns the child PID.
func startBin(ctx context.Context, runnerPath, binName, runDir, logPath string, env []string) (int, error) {
	script := runnerPath
	if resolved, err := filepath.EvalSymlinks(runnerPath); err == nil {
		script = resolved
	}
	// nix build --out-link symlinks to the store DIRECTORY holding bin/*;
	// execing a directory is EACCES, so resolve it.
	if fi, err := os.Stat(script); err == nil && fi.IsDir() {
		script = filepath.Join(script, "bin", binName)
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
	if len(env) > 0 {
		cmd.Env = env
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}

// WaitForSocket polls for path to exist as a unix socket, checking every
// interval until it appears or timeout elapses. Used to wait for
// virtiofsd's socket(s) to be ready before starting the VM that connects
// to them (cloud-hypervisor's docs don't promise a connect retry).
func WaitForSocket(path string, interval, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runtime: timed out waiting for socket %s", path)
		}
		time.Sleep(interval)
	}
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
