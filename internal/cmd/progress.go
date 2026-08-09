package cmd

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// progress reports transient step lines during long-running commands like
// `up`. On a real terminal each Step overwrites the previous one in place,
// so the scrollback ends up holding only the final, permanent output (e.g.
// printStore's table). Piped/redirected output (tests, logs, CI) instead
// gets every step on its own line, since there's no cursor to rewind.
type progress struct {
	out         io.Writer
	interactive bool
	// width is the terminal's column count, or 0 if unknown. Step lines
	// longer than width are truncated: a line that wraps to a second
	// terminal row breaks the overwrite trick, since \r and \x1b[2K only
	// rewind/erase the row the cursor is on, leaving stale text above.
	width int
}

func newProgress(out io.Writer, interactive bool, width int) *progress {
	return &progress{out: out, interactive: interactive, width: width}
}

// isTerminal reports whether w is a character device (a real terminal), as
// opposed to a file, pipe, or in-memory buffer.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// terminalWidth returns w's column count, or 0 if it can't be determined
// (not a terminal, or the ioctl fails).
func terminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}

// Step reports a transient progress message.
func (p *progress) Step(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !p.interactive {
		fmt.Fprintln(p.out, msg)
		return
	}
	if p.width > 0 && len(msg) > p.width {
		msg = msg[:p.width]
	}
	fmt.Fprintf(p.out, "\r\x1b[2K%s", msg)
}

// Done clears the transient line in interactive mode, so whatever prints
// next (a permanent line, the final table) starts on a clean row. No-op
// otherwise: non-interactive steps are already permanent lines.
func (p *progress) Done() {
	if p.interactive {
		fmt.Fprint(p.out, "\r\x1b[2K")
	}
}
