package main

import (
	"io"
	"os"
	"strconv"

	"github.com/mattn/go-isatty"
)

// defaultColumns is the width of the bar when neither the terminal nor COLUMNS
// says.
const defaultColumns = 80

// terminalWriter is implemented by a writer that knows whether it is a terminal;
// tests use it for a standard error that is one.
type terminalWriter interface {
	terminal() (isTTY bool, cols int)
}

// terminalOf reports whether w is a terminal and, if the system says, how many
// columns it has (0 when it does not). Only a file can be a terminal.
func terminalOf(w io.Writer) (isTTY bool, cols int) {
	if t, ok := w.(terminalWriter); ok {
		return t.terminal()
	}
	f, ok := w.(*os.File)
	if !ok {
		return false, 0
	}
	fd := f.Fd()
	if !isatty.IsTerminal(fd) && !isatty.IsCygwinTerminal(fd) {
		return false, 0
	}
	return true, fdColumns(fd)
}

// columnsOf is the width of the bar line: the terminal's, else COLUMNS, else 80.
func columnsOf(termCols int, getenv func(string) string) int {
	if termCols > 0 {
		return termCols
	}
	if n, err := strconv.Atoi(getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return defaultColumns
}
