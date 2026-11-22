//go:build unix

package main

import "golang.org/x/sys/unix"

// fdColumns is the width of the terminal on fd, or 0 when it cannot be read.
func fdColumns(fd uintptr) int {
	ws, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}
