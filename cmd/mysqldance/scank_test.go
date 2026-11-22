package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

const quasiRules = "version: 1\nrules:\n  shop.users.zip:\n    quasi: true\n"

func TestScanKIsASubcommandOnlyAsTheFirstArgument(t *testing.T) {
	// A database called scan-k is dumped when the flags come first.
	got, err := parseArgs([]string{"-o", "out", "scan-k"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DBs) != 1 || got.DBs[0] != "scan-k" {
		t.Errorf("DBs = %v, want [scan-k]", got.DBs)
	}
	// A longer name is not the subcommand.
	var out, errOut bytes.Buffer
	if code := run([]string{"scan-k2"}, noEnv, &out, &errOut); code != 1 {
		t.Errorf("run(scan-k2) = %d, want the dump's 1 (it needs -o)", code)
	}
}

func TestParseScanK(t *testing.T) {
	rules := writeFile(t, "rules.yaml", quasiRules, 0o600)
	got, err := parseScanK([]string{"--mask-rules", rules, "--k", "5", "--format", "json", "--exclude-table", "shop.log", "shop", "blog"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got.K != 5 || got.format != "json" || strings.Join(got.DBs, ",") != "shop,blog" ||
		strings.Join(got.ExcludeTables, ",") != "shop.log" || got.Rules == nil {
		t.Errorf("options = %+v", got)
	}
	got, err = parseScanK([]string{"--mask-rules", rules, "shop"}, noEnv, io.Discard)
	if err != nil || got.K != 0 || got.format != "table" {
		t.Errorf("defaults: %+v, %v", got, err)
	}
}

func TestParseScanKRefusals(t *testing.T) {
	rules := writeFile(t, "rules.yaml", quasiRules, 0o600)
	bad := writeFile(t, "bad.yaml", "version: 1\nrules:\n  shop.users.zip:\n    quasi: true\n    paths:\n      $.a:\n        use: nullify\n", 0o600)
	for name, args := range map[string][]string{
		"no rules":       {"shop"},
		"no database":    {"--mask-rules", rules},
		"k is zero":      {"--mask-rules", rules, "--k", "0", "shop"},
		"k is negative":  {"--mask-rules", rules, "--k", "-3", "shop"},
		"bad format":     {"--mask-rules", rules, "--format", "csv", "shop"},
		"output flag":    {"--mask-rules", rules, "-o", "out", "shop"},
		"bad rules":      {"--mask-rules", bad, "shop"},
		"missing rules":  {"--mask-rules", filepath.Join(t.TempDir(), "none.yaml"), "shop"},
		"bad exclude":    {"--mask-rules", rules, "--exclude-table", "nodot", "shop"},
		"bad ssl":        {"--mask-rules", rules, "--ssl-mode", "maybe", "shop"},
		"bad log level":  {"--mask-rules", rules, "--log-level", "loud", "shop"},
		"bad port range": {"--mask-rules", rules, "-P", "0", "shop"},
	} {
		if _, err := parseScanK(args, noEnv, io.Discard); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRunScanKExitCodes(t *testing.T) {
	rules := writeFile(t, "rules.yaml", quasiRules, 0o600)
	bad := writeFile(t, "bad.yaml", "version: 1\nrules:\n  shop.users.zip:\n    use: nope\n", 0o600)
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"help", []string{"--help"}, 0},
		{"usage error", []string{"shop"}, 2},
		{"bad rule file", []string{"--mask-rules", bad, "shop"}, 2},
		{"cannot connect", []string{"--mask-rules", rules, "--socket", filepath.Join(t.TempDir(), "no.sock"), "shop"}, 2},
	} {
		var out, errOut bytes.Buffer
		if got := runScanK(tt.args, noEnv, &out, &errOut); got != tt.want {
			t.Errorf("%s: exit %d, want %d\nstderr: %s", tt.name, got, tt.want, errOut.String())
		}
		if tt.want != 0 && out.Len() != 0 {
			t.Errorf("%s: a failed run wrote to standard output: %q", tt.name, out.String())
		}
	}
}
