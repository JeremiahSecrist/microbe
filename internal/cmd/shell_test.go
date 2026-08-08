package cmd

import "testing"

func TestShellCmdRequiresExactlyOneArg(t *testing.T) {
	cmd := newShellCmd()
	if err := cmd.Args(cmd, nil); err == nil {
		t.Error("shell with 0 args = nil error, want error")
	}
	if err := cmd.Args(cmd, []string{"web", "extra"}); err == nil {
		t.Error("shell with 2 args = nil error, want error")
	}
	if err := cmd.Args(cmd, []string{"web"}); err != nil {
		t.Errorf("shell with 1 arg = %v, want nil", err)
	}
}
