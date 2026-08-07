// Package cmdrun abstracts executing privileged host commands (ip, iptables,
// qemu-img) so higher layers can run for real, in dry-run mode, or against a
// fake in tests.
package cmdrun

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner executes a host command. Implementations return a descriptive error
// when the command fails.
type Runner func(name string, args ...string) error

// geteuid is a seam so tests can simulate running as root or not.
var geteuid = func() int { return os.Geteuid() }

// Shell runs commands for real.
func Shell() Runner {
	return func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return nil
	}
}

// Dry prints commands to w without executing them.
func Dry(w io.Writer) Runner {
	return func(name string, args ...string) error {
		fmt.Fprintf(w, "%s %s\n", name, strings.Join(args, " "))
		return nil
	}
}

// Sudo wraps inner so the given privileged command names execute via
// `sudo -n` (non-interactive) when the process is not running as root. Other
// commands run unchanged. Requires a sudoers rule granting the caller NOPASSWD
// access to those commands, as installed by the microbe host module for the
// microbe group.
func Sudo(inner Runner, privileged ...string) Runner {
	priv := make(map[string]bool, len(privileged))
	for _, p := range privileged {
		priv[p] = true
	}
	needSudo := geteuid() != 0
	return func(name string, args ...string) error {
		if needSudo && priv[name] {
			return inner("sudo", append([]string{"-n", name}, args...)...)
		}
		return inner(name, args...)
	}
}
