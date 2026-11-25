package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mysqldance/internal/dump"
)

const goodRules = "version: 1\nrules:\n  shop.users.email:\n    use: nullify\n"

func writeFile(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseArgsMaskRules(t *testing.T) {
	rules := writeFile(t, "rules.yaml", goodRules, 0o600)
	got, err := parseArgs([]string{"--mask-rules", rules, "-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mask == nil || len(got.Mask.Entries) != 1 {
		t.Fatalf("Mask = %+v", got.Mask)
	}
	// fixed and nullify need no salt, so none is needed to run.
	if got.MaskSalt != nil {
		t.Error("a salt appeared from nowhere")
	}
	got, err = parseArgs([]string{"-o", "out", "d"}, noEnv)
	if err != nil || got.Mask != nil {
		t.Fatalf("without --mask-rules: Mask = %v, err = %v", got.Mask, err)
	}
}

func TestParseArgsMaskRulesErrors(t *testing.T) {
	bad := writeFile(t, "bad.yaml", "version: 1\nrules:\n  shop.users.email:\n    use: nope\n", 0o600)
	if _, err := parseArgs([]string{"--mask-rules", bad, "-o", "out", "d"}, noEnv); err == nil || !strings.Contains(err.Error(), `unknown transform "nope"`) {
		t.Errorf("a bad rule file: err = %v", err)
	}
	if _, err := parseArgs([]string{"--mask-rules", filepath.Join(t.TempDir(), "none.yaml"), "-o", "out", "d"}, noEnv); err == nil {
		t.Error("a missing rule file was accepted")
	}
}

func TestParseArgsStrictColumns(t *testing.T) {
	// Without rules there is nothing to be strict about: a usage error.
	_, err := parseArgs([]string{"--strict-columns", "-o", "out", "d"}, noEnv)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
	rules := writeFile(t, "rules.yaml", goodRules, 0o600)
	got, err := parseArgs([]string{"--mask-rules", rules, "--strict-columns", "-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !got.StrictColumns {
		t.Error("StrictColumns is not set")
	}
	got, err = parseArgs([]string{"--mask-rules", rules, "-o", "out", "d"}, noEnv)
	if err != nil || got.StrictColumns {
		t.Fatalf("by default StrictColumns = %v, err = %v; want off", got.StrictColumns, err)
	}
}

func TestParseArgsStrictRules(t *testing.T) {
	// Without rules there is nothing to be strict about: a usage error.
	_, err := parseArgs([]string{"--strict-rules", "-o", "out", "d"}, noEnv)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
	rules := writeFile(t, "rules.yaml", goodRules, 0o600)
	got, err := parseArgs([]string{"--mask-rules", rules, "--strict-rules", "-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !got.StrictRules {
		t.Error("StrictRules is not set")
	}
	got, err = parseArgs([]string{"--mask-rules", rules, "-o", "out", "d"}, noEnv)
	if err != nil || got.StrictRules {
		t.Fatalf("by default StrictRules = %v, err = %v; want off", got.StrictRules, err)
	}
}

// A dump that is complete and in place but has a rule that matched no row is not
// logged as a failed dump: the exit code is 1, the record says what happened.
func TestLogRunError(t *testing.T) {
	for _, tt := range []struct {
		name, want, wantNot string
		err                 error
	}{
		{"failure", `msg="dump failed"`, "in place", errors.New("boom")},
		{"unused rule", `msg="masking rule matched no row"`, "dump failed", fmt.Errorf("--strict-rules: %w: 1 rule(s)", dump.ErrUnusedRules)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logRunError(slog.New(slog.NewTextHandler(&buf, nil)), tt.err)
			out := buf.String()
			if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, tt.want) || strings.Contains(out, tt.wantNot) {
				t.Errorf("log = %s, want %s and not %s", out, tt.want, tt.wantNot)
			}
		})
	}
}

func TestParseArgsSaltFileNeedsRules(t *testing.T) {
	salt := writeFile(t, "salt", "0123456789abcdef\n", 0o600)
	_, err := parseArgs([]string{"--salt-file", salt, "-o", "out", "d"}, noEnv)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

// A salt that is given is checked even when no transform uses it; the one from
// the environment is ignored when there are no rules.
func TestParseArgsSaltIsAlwaysChecked(t *testing.T) {
	rules := writeFile(t, "rules.yaml", goodRules, 0o600)
	short := writeFile(t, "short", "tooshort\n", 0o600)
	if _, err := parseArgs([]string{"--mask-rules", rules, "--salt-file", short, "-o", "out", "d"}, noEnv); err == nil || !strings.Contains(err.Error(), "at least 16 bytes") {
		t.Errorf("a short salt file: err = %v", err)
	}
	env := func(k string) string {
		if k == "MYSQLDANCE_SALT" {
			return "tooshort"
		}
		return ""
	}
	if _, err := parseArgs([]string{"--mask-rules", rules, "-o", "out", "d"}, env); err == nil || !strings.Contains(err.Error(), "at least 16 bytes") {
		t.Errorf("a short salt in the environment: err = %v", err)
	}
	if _, err := parseArgs([]string{"-o", "out", "d"}, env); err != nil {
		t.Errorf("the environment salt without rules: %v", err)
	}
	good := writeFile(t, "good", "0123456789abcdef\n", 0o600)
	got, err := parseArgs([]string{"--mask-rules", rules, "--salt-file", good, "-o", "out", "d"}, noEnv)
	if err != nil || got.MaskSalt == nil {
		t.Fatalf("MaskSalt = %v, err = %v", got.MaskSalt, err)
	}
	if runtime.GOOS != "windows" {
		open := writeFile(t, "open", "0123456789abcdef\n", 0o644)
		if err := os.Chmod(open, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := parseArgs([]string{"--mask-rules", rules, "--salt-file", open, "-o", "out", "d"}, noEnv); err == nil {
			t.Error("a salt file that others can read was accepted")
		}
	}
}
