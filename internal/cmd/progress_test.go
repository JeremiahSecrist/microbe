package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestProgressStepNonInteractivePrintsEachLineNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, 0)
	p.Step("rendered %s", ".")
	p.Step("started db (pid %d)", 123)
	p.Done()

	got := buf.String()
	want := "rendered .\nstarted db (pid 123)\n"
	if got != want {
		t.Errorf("non-interactive output = %q, want %q", got, want)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-interactive output should carry no ANSI escapes, got %q", got)
	}
}

func TestProgressStepInteractiveOverwritesLine(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, true, 0)
	p.Step("rendered %s", ".")
	p.Step("started db (pid %d)", 123)

	got := buf.String()
	want := "\r\x1b[2Krendered .\r\x1b[2Kstarted db (pid 123)"
	if got != want {
		t.Errorf("interactive output = %q, want %q", got, want)
	}
}

func TestProgressDoneClearsLineInteractive(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, true, 0)
	p.Step("started db (pid %d)", 123)
	p.Done()

	got := buf.String()
	want := "\r\x1b[2Kstarted db (pid 123)\r\x1b[2K"
	if got != want {
		t.Errorf("output after Done = %q, want %q", got, want)
	}
}

// TestProgressStepTruncatesToTerminalWidth guards against the real-terminal
// bug where a step line longer than the terminal wraps onto a second row:
// \r only rewinds to the start of that wrapped row and \x1b[2K only erases
// it, so the next Step leaves stale text from the row above. Truncating to
// width keeps every step a single row so overwrite always fully replaces it.
func TestProgressStepTruncatesToTerminalWidth(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, true, 20)
	p.Step("db -> /nix/store/sgy6pmji2c9hdkqcx7815b1svyh6hhv3-microvm-cloud-hypervisor-db")

	got := buf.String()
	want := "\r\x1b[2Kdb -> /nix/store/sgy"
	if got != want {
		t.Errorf("truncated output = %q, want %q", got, want)
	}
	if len(got)-len("\r\x1b[2K") != 20 {
		t.Errorf("step line length = %d, want 20", len(got)-len("\r\x1b[2K"))
	}
}

func TestProgressStepNoTruncationWhenWidthUnknown(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, true, 0)
	longMsg := "db -> /nix/store/sgy6pmji2c9hdkqcx7815b1svyh6hhv3-microvm-cloud-hypervisor-db"
	p.Step("%s", longMsg)

	want := "\r\x1b[2K" + longMsg
	if buf.String() != want {
		t.Errorf("output = %q, want %q (width 0 = no truncation)", buf.String(), want)
	}
}

func TestProgressDoneNoopNonInteractive(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, 0)
	p.Step("started db (pid %d)", 123)
	before := buf.String()
	p.Done()
	if buf.String() != before {
		t.Errorf("Done() should be a no-op in non-interactive mode, output changed to %q", buf.String())
	}
}
