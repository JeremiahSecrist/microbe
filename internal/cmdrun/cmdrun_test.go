package cmdrun

import (
	"bytes"
	"strings"
	"testing"
)

func TestDryPrintsCommand(t *testing.T) {
	var buf bytes.Buffer
	r := Dry(&buf)
	if err := r("ip", "link", "add", "br-x", "type", "bridge"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "ip link add br-x type bridge") {
		t.Errorf("Dry output = %q, want the command echoed", got)
	}
}

func TestShellRunsRealCommand(t *testing.T) {
	r := Shell()
	if err := r("true"); err != nil {
		t.Errorf("Shell(true): %v", err)
	}
	if err := r("sh", "-c", "exit 3"); err == nil {
		t.Error("Shell(failing command): want error, got nil")
	}
}
