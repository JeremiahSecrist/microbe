package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <service> [cmd...]",
		Short: "Run a command inside a service over vsock",
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

// execRun is non-interactive (no pty), matching the prior ssh-based exec's
// behavior: ssh was never given `-t`, so even an interactive command run
// via `exec` (as opposed to `shell`) got a plain pipe, not a terminal.
func execRun(opts execOptions) error {
	addr, err := resolveGuestVsock(opts.file, opts.service)
	if err != nil {
		return err
	}
	return runAgentSession(addr, opts.command, false, opts.stdin, opts.stdout, opts.stderr)
}
