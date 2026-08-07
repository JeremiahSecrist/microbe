package cmd

import (
	"errors"
	"fmt"
	"net"
	"os"

	"microbe/internal/provisiond"

	"github.com/spf13/cobra"
)

// newProvisiondCmd returns the hidden `microbe provisiond` subcommand run by
// the systemd service unit (socket-activated). It serves the netlink-backed
// Ops over the microbe unix socket.
func newProvisiondCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "provisiond",
		Short:  "Run the root provisioning daemon (systemd)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			srv, err := socketActivatedServer()
			if err != nil {
				return err
			}
			if srv == nil {
				srv, err = provisiond.ListenUnix(provisiond.SocketPath, provisiond.NetOps{})
				if err != nil {
					return err
				}
			}
			if err := srv.Serve(); err != nil {
				return fmt.Errorf("provisiond: serve: %w", err)
			}
			return nil
		},
	}
}

// systemdListenFDStart is SD_LISTEN_FDS_START: systemd always hands the
// first socket-activated fd to the child process as fd 3.
const systemdListenFDStart = 3

// socketActivatedServer returns the provisiond server wired to systemd's
// passed-in socket (LISTEN_FDS), or nil if not socket-activated.
func socketActivatedServer() (*provisiond.Server, error) {
	if os.Getenv("LISTEN_PID") == "" || os.Getenv("LISTEN_FDS") == "" {
		return nil, nil
	}
	fds := 0
	if _, err := fmt.Sscanf(os.Getenv("LISTEN_FDS"), "%d", &fds); err != nil || fds < 1 {
		return nil, errors.New("provisiond: invalid LISTEN_FDS")
	}
	sockFile := os.NewFile(systemdListenFDStart, "microbe.sock")
	if sockFile == nil {
		return nil, errors.New("provisiond: no socket passed by systemd (fd 3)")
	}
	ln, err := net.FileListener(sockFile)
	if err != nil {
		sockFile.Close()
		return nil, fmt.Errorf("provisiond: adopt fd 3: %w", err)
	}
	return provisiond.NewServer(ln, provisiond.NetOps{}), nil
}
