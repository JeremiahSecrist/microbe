package cmd

import "golang.org/x/sys/unix"

// makeRaw puts fd into termios raw mode (cfmakeraw semantics: no line
// buffering, no local echo, no signal generation from Ctrl-C/Ctrl-Z, 8-bit
// clean) so an interactive `microbe shell` sees every keystroke exactly as
// typed and lets the guest's own shell handle it, matching a normal SSH or
// serial console session. Returns the prior state so the caller can
// restore it (see restoreTerm) once the session ends.
func makeRaw(fd int) (*unix.Termios, error) {
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return old, nil
}

// restoreTerm restores a termios state saved by makeRaw.
func restoreTerm(fd int, state *unix.Termios) {
	unix.IoctlSetTermios(fd, unix.TCSETS, state)
}

// termSize reads fd's current terminal size.
func termSize(fd int) (rows, cols uint16, err error) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return ws.Row, ws.Col, nil
}
