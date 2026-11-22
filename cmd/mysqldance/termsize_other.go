//go:build !unix && !windows

package main

// fdColumns is 0 here: the width comes from COLUMNS or the default.
func fdColumns(uintptr) int { return 0 }
