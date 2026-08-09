package cmd

import (
	"bytes"
	"strings"
	"testing"

	"microbe/internal/state"
)

func testStore() *state.Store {
	return &state.Store{
		Services: map[string]state.ServiceState{
			"db":       {Status: serviceStatusRunning, PID: 111, IP: map[string]string{"backend": "192.168.51.2"}},
			"jump":     {Status: serviceStatusDegraded, PID: 222, IP: map[string]string{"backend": "192.168.51.4"}},
			"unstable": {Status: serviceStatusStopped, PID: 0, IP: map[string]string{}},
		},
	}
}

func TestPrintStoreNonInteractiveHasNoColorEscapes(t *testing.T) {
	var buf bytes.Buffer
	printStore(&buf, testStore(), false)
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("non-interactive printStore should carry no ANSI escapes, got %q", buf.String())
	}
}

func TestPrintStoreInteractiveColorsStatus(t *testing.T) {
	var buf bytes.Buffer
	printStore(&buf, testStore(), true)
	got := buf.String()

	if !strings.Contains(got, "\x1b[32mrunning\x1b[0m") {
		t.Errorf("expected green running status, got %q", got)
	}
	if !strings.Contains(got, "\x1b[31mdegraded\x1b[0m") {
		t.Errorf("expected red degraded status, got %q", got)
	}
	if !strings.Contains(got, "\x1b[90mstopped\x1b[0m") {
		t.Errorf("expected dim stopped status, got %q", got)
	}
}
