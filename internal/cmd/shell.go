package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// newShellCmd is `exec <service>` with no command and a pty: a real
// interactive login shell (incus's `shell` alias is the same idea --
// `exec @ARGS@ -- su -l`), unlike `exec`, which never allocates one.
func newShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell <service>",
		Short: "Open an interactive shell in a service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return shellRun(execOptions{
				file:    file,
				service: args[0],
				stdin:   os.Stdin,
				stdout:  os.Stdout,
				stderr:  os.Stderr,
			})
		},
	}
}

func shellRun(opts execOptions) error {
	addr, err := resolveGuestVsock(opts.file, opts.service)
	if err != nil {
		return err
	}
	return runAgentSession(addr, nil, true, opts.stdin, opts.stdout, opts.stderr)
}
