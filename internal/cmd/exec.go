package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"microbe/internal/config"
	"microbe/internal/datadir"
	"microbe/internal/state"
)

func newExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <service> [cmd...]",
		Short: "Run a command inside a service over SSH",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return execRun(execOptions{
				file:    file,
				service: args[0],
				command: args[1:],
				stdin:   os.Stdin,
				stdout:  os.Stdout,
				stderr:  os.Stderr,
			})
		},
	}
}

type execOptions struct {
	file, service  string
	command        []string
	stdin          *os.File
	stdout, stderr *os.File
}

// runSSH runs the ssh(1) client with args, wiring stdio straight through
// (not via io.Reader/Writer) so an interactive `microbe shell` gets a real
// TTY. A seam so tests can assert on the resolved args without shelling out.
var runSSH = func(args []string, stdin, stdout, stderr *os.File) error {
	c := exec.Command("ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	return c.Run()
}

func execRun(opts execOptions) error {
	ip, privPath, err := resolveGuestSSH(opts.file, opts.service)
	if err != nil {
		return err
	}
	return runSSH(sshArgs(privPath, ip, opts.command), opts.stdin, opts.stdout, opts.stderr)
}

// resolveGuestSSH loads the compose config (for the service's declared
// network order and stack name) and the running state (for its assigned
// IPs) to find the address to reach svc on, plus the CLI's own keypair for
// it (see internal/sshkey, injected into every guest by up.go/guest-base.nix).
func resolveGuestSSH(file, svc string) (ip, privKeyPath string, err error) {
	cfg, err := config.Load(file)
	if err != nil {
		return "", "", err
	}
	svcCfg, ok := cfg.Services[svc]
	if !ok {
		return "", "", fmt.Errorf("no service %q", svc)
	}
	if len(svcCfg.Networks) == 0 {
		return "", "", fmt.Errorf("service %q has no networks", svc)
	}
	base := datadir.Dir(cfg.Name)
	store, err := state.Load(filepath.Join(base, "state.json"))
	if err != nil {
		return "", "", err
	}
	svcState, ok := store.Services[svc]
	if !ok {
		return "", "", fmt.Errorf("service %q is not running", svc)
	}
	primary := svcCfg.Networks[0].Name
	ip, ok = svcState.IP[primary]
	if !ok {
		return "", "", fmt.Errorf("service %q has no IP on network %q", svc, primary)
	}
	return ip, filepath.Join(base, "ssh", "id_ed25519"), nil
}

// sshArgs builds the ssh(1) argument list to reach a guest as root with the
// CLI's own keypair. Host key checking is disabled: these are ephemeral,
// CLI-managed VMs the user doesn't otherwise track known_hosts for, so
// StrictHostKeyChecking would just fail or prompt on every fresh VM.
func sshArgs(privKeyPath, ip string, command []string) []string {
	args := []string{
		"-i", privKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"root@" + ip,
	}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	return args
}
