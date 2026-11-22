//go:build windows

package main

import "golang.org/x/sys/windows"

// fdColumns is the width of the console on fd, or 0 when it cannot be read.
func fdColumns(fd uintptr) int {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info); err != nil {
		return 0
	}
	return int(info.Window.Right-info.Window.Left) + 1
}
