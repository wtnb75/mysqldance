package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// parseLog parses args and returns the logger together with the stderr buffer
// it writes to.
func parseLog(t *testing.T, args ...string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var stderr bytes.Buffer
	opt, err := parseArgsTo(append(args, "-o", "out", "d"), noEnv, &stderr)
	if err != nil {
		t.Fatalf("parseArgsTo(%q): %v\n%s", args, err, stderr.String())
	}
	if opt.Log == nil {
		t.Fatal("Options.Log is nil")
	}
	stderr.Reset()
	return opt.Log, &stderr
}

// The default is the info level in the text format.
func TestLogDefaults(t *testing.T) {
	l, buf := parseLog(t)
	ctx := context.Background()
	if !l.Enabled(ctx, slog.LevelInfo) || l.Enabled(ctx, slog.LevelDebug) {
		t.Errorf("default level: info enabled = %v, debug enabled = %v, want true and false",
			l.Enabled(ctx, slog.LevelInfo), l.Enabled(ctx, slog.LevelDebug))
	}
	l.Warn("something odd", "db", "d")
	got := buf.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, `msg="something odd"`) || !strings.Contains(got, "db=d") {
		t.Errorf("text output = %q", got)
	}
}

func TestLogLevel(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		level string
		want  map[slog.Level]bool
	}{
		{"debug", map[slog.Level]bool{slog.LevelDebug: true, slog.LevelInfo: true, slog.LevelWarn: true, slog.LevelError: true}},
		{"info", map[slog.Level]bool{slog.LevelDebug: false, slog.LevelInfo: true, slog.LevelWarn: true, slog.LevelError: true}},
		{"warn", map[slog.Level]bool{slog.LevelDebug: false, slog.LevelInfo: false, slog.LevelWarn: true, slog.LevelError: true}},
		{"error", map[slog.Level]bool{slog.LevelDebug: false, slog.LevelInfo: false, slog.LevelWarn: false, slog.LevelError: true}},
	} {
		t.Run(tt.level, func(t *testing.T) {
			l, _ := parseLog(t, "--log-level", tt.level)
			for lv, want := range tt.want {
				if got := l.Enabled(ctx, lv); got != want {
					t.Errorf("--log-level %s: %v enabled = %v, want %v", tt.level, lv, got, want)
				}
			}
		})
	}
}

func TestLogFormatJSON(t *testing.T) {
	l, buf := parseLog(t, "--log-format", "json")
	l.Warn("something odd", "db", "d")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if m["level"] != "WARN" || m["msg"] != "something odd" || m["db"] != "d" {
		t.Errorf("JSON output = %v", m)
	}
}

func TestLogFlagsRejectBadValues(t *testing.T) {
	for _, args := range [][]string{
		{"--log-level", "trace"},
		{"--log-level", ""},
		{"--log-format", "yaml"},
	} {
		var stderr bytes.Buffer
		if _, err := parseArgsTo(append(args, "-o", "out", "d"), noEnv, &stderr); err == nil {
			t.Errorf("parseArgsTo(%q) succeeded, want an error", args)
		}
		if !strings.Contains(stderr.String(), "--log-") {
			t.Errorf("parseArgsTo(%q) stderr = %q, want a message naming the flag", args, stderr.String())
		}
	}
}

// failingRun runs a dump against a closed port, so dump.Run fails without a
// server, and returns the exit code and both output buffers.
func failingRun(t *testing.T, extra ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	args := append(extra, "--host", "127.0.0.1", "--port", "1", "-o", t.TempDir(), "d")
	var stdout, stderr bytes.Buffer
	code := run(args, noEnv, &stdout, &stderr)
	return code, &stdout, &stderr
}

// The final error of a failed dump goes through the logger: a text record by
// default, and nothing but records in the JSON format.
func TestRunFinalErrorUsesLogger(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		code, stdout, stderr := failingRun(t)
		if code != 1 {
			t.Fatalf("run = %d, want 1", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		got := stderr.String()
		if !strings.Contains(got, "level=ERROR") || strings.HasPrefix(got, "mysqldance:") {
			t.Errorf("stderr = %q, want a level=ERROR record", got)
		}
	})
	t.Run("json", func(t *testing.T) {
		code, stdout, stderr := failingRun(t, "--log-format", "json")
		if code != 1 {
			t.Fatalf("run = %d, want 1", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		last := lines[len(lines)-1]
		var m map[string]any
		if err := json.Unmarshal([]byte(last), &m); err != nil {
			t.Fatalf("last line is not JSON: %v\n%s", err, stderr.String())
		}
		if m["level"] != "ERROR" || m["error"] == nil {
			t.Errorf("last record = %v, want level ERROR with an error attribute", m)
		}
	})
}

// The final error is the highest level there is, so --log-level error keeps it.
func TestRunFinalErrorSurvivesLevelError(t *testing.T) {
	code, _, stderr := failingRun(t, "--log-level", "error")
	if code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "level=ERROR") {
		t.Errorf("stderr = %q, want a level=ERROR record", stderr.String())
	}
}

// A usage error keeps the plain message, and the error is not logged twice.
func TestRunUsageErrorStaysPlain(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--log-format", "json"}, noEnv, &stdout, &stderr); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	got := stderr.String()
	if !strings.HasPrefix(got, "mysqldance: ") || strings.Contains(got, "ERROR") {
		t.Errorf("stderr = %q, want only the plain usage message", got)
	}
}

// The logger writes to the stderr given to run and nothing reaches stdout.
func TestRunLogsToStderrOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// No output directory: dump fails at once, without a server.
	if code := run([]string{"--log-level", "debug"}, noEnv, &stdout, &stderr); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}
