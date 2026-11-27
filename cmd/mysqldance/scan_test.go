package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

const ownRule = "[[rules]]\nid = \"own\"\nregex = 'zzz+'\n"

func TestScanIsASubcommandOnlyAsTheFirstArgument(t *testing.T) {
	// A database called scan is dumped when the flags come first.
	got, err := parseArgs([]string{"-o", "out", "scan"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DBs) != 1 || got.DBs[0] != "scan" {
		t.Errorf("DBs = %v, want [scan]", got.DBs)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"scan2"}, noEnv, &out, &errOut); code != 1 {
		t.Errorf("run(scan2) = %d, want the dump's 1 (it needs -o)", code)
	}
}

func TestParseScanDefaultsToTheBuiltinPII(t *testing.T) {
	got, err := parseScan([]string{"--exclude-table", "shop.log", "--format", "json", "shop", "blog"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got.format != "json" || strings.Join(got.DBs, ",") != "shop,blog" || strings.Join(got.ExcludeTables, ",") != "shop.log" {
		t.Errorf("options = %+v", got)
	}
	if m := got.Config.Scan("shop.users.mail", "a@example.com"); len(m) != 1 || m[0].RuleID != "pii-email" {
		t.Errorf("pii-email did not match: %+v", m)
	}
	// pii-columns (by column name) is used only when a config asks for it.
	if m := got.Config.Scan("shop.users.last_name", "hunter2"); len(m) != 0 {
		t.Errorf("pii-columns is on by default: %+v", m)
	}
	def, err := parseScan([]string{"shop"}, noEnv, io.Discard)
	if err != nil || def.format != "table" {
		t.Errorf("default: %+v, %v", def, err)
	}
}

func TestParseScanConfigReplacesTheDefault(t *testing.T) {
	// A config without useBuiltin uses no built-in rule.
	cfg := writeFile(t, "scan.toml", ownRule, 0o600)
	got, err := parseScan([]string{"--scan-config", cfg, "shop"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if m := got.Config.Scan("shop.t.c", "a@example.com"); len(m) != 0 {
		t.Errorf("a built-in rule is on: %+v", m)
	}
	if m := got.Config.Scan("shop.t.c", "zzzz"); len(m) != 1 || m[0].RuleID != "own" {
		t.Errorf("own rule: %+v", m)
	}
	// A config that asks for pii-columns gets it.
	cols := writeFile(t, "cols.toml", "[extend]\nuseBuiltin = [\"pii-columns\"]\n", 0o600)
	got, err = parseScan([]string{"--scan-config", cols, "shop"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if m := got.Config.Scan("shop.users.last_name", "hunter2"); len(m) == 0 {
		t.Error("pii-columns was not used")
	}
}

func TestParseScanRefusals(t *testing.T) {
	bad := writeFile(t, "bad.toml", "[[rules]]\nid = \"r\"\nregex = '('\n", 0o600)
	for name, args := range map[string][]string{
		"no database":    {},
		"bad format":     {"--format", "csv", "shop"},
		"output flag":    {"-o", "out", "shop"},
		"bad config":     {"--scan-config", bad, "shop"},
		"missing config": {"--scan-config", filepath.Join(t.TempDir(), "none.toml"), "shop"},
		"bad exclude":    {"--exclude-table", "nodot", "shop"},
		"bad ssl":        {"--ssl-mode", "maybe", "shop"},
		"bad log level":  {"--log-level", "loud", "shop"},
		"bad port range": {"-P", "0", "shop"},
	} {
		if _, err := parseScan(args, noEnv, io.Discard); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRunScanExitCodes(t *testing.T) {
	bad := writeFile(t, "bad.toml", "[[rules]]\nid = \"r\"\nregex = '('\n", 0o600)
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"help", []string{"--help"}, 0},
		{"usage error", []string{}, 2},
		{"bad config", []string{"--scan-config", bad, "shop"}, 2},
		{"cannot connect", []string{"--socket", filepath.Join(t.TempDir(), "no.sock"), "shop"}, 2},
	} {
		var out, errOut bytes.Buffer
		if got := runScan(tt.args, noEnv, &out, &errOut); got != tt.want {
			t.Errorf("%s: exit %d, want %d\nstderr: %s", tt.name, got, tt.want, errOut.String())
		}
		if tt.want != 0 && out.Len() != 0 {
			t.Errorf("%s: a failed run wrote to standard output: %q", tt.name, out.String())
		}
	}
}
