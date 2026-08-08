// Package chapi is a minimal client for cloud-hypervisor's REST API, reached
// over its --api-socket unix socket. It exists so microbe can ask the
// hypervisor itself whether a VM is actually running, instead of trusting
// only the PID it recorded when it launched the process.
package chapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Info is the subset of cloud-hypervisor's vm.info response microbe cares
// about.
type Info struct {
	State string `json:"state"`
}

// VMState queries socketPath (a cloud-hypervisor --api-socket file) for the
// VM's actual state ("Running", "Paused", "Created", "BreakPoint", ...).
//
// Returns ("", nil) if socketPath doesn't exist: a torn-down VM, not an
// error condition callers need to handle specially. A socket file that
// exists but refuses connections (the VMM died without cleaning up after
// itself) is an error.
func VMState(socketPath string) (string, error) {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return "", nil
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 2 * time.Second,
	}

	resp, err := client.Get("http://unix/api/v1/vm.info")
	if err != nil {
		return "", fmt.Errorf("chapi: query %s: %w", socketPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chapi: %s: unexpected status %s", socketPath, resp.Status)
	}

	var info Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("chapi: decode response from %s: %w", socketPath, err)
	}
	return info.State, nil
}
