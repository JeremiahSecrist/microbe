// Package cmdrun abstracts executing host commands (qemu-img) so higher
// layers can run for real, in dry-run mode, or against a fake in tests.
package cmdrun

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Runner executes a host command. Implementations return a descriptive error
// when the command fails.
type Runner func(name string, args ...string) error

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
