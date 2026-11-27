package main

import (
	"bytes"
	"strings"
	"testing"
)

// The dump's --help names every subcommand, so that a user can find them.
func TestDumpHelpNamesTheSubcommands(t *testing.T) {
	var out, errOut bytes.Buffer
	run([]string{"--help"}, noEnv, &out, &errOut)
	help := out.String() + errOut.String()
	for _, name := range []string{subcommandScanK, subcommandScan} {
		if !strings.Contains(help, "mysqldance "+name+" --help") {
			t.Errorf("help does not name the %s subcommand:\n%s", name, help)
		}
	}
}
