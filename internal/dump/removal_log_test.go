package dump

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"mysqldance/internal/naming"
)

// Tests of the messages for the files removed from the output directory (B-089).

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoveStaleLogsEachRemovedFile(t *testing.T) {
	dir := t.TempDir()
	plan := naming.NewPlan("app", []string{"a"})
	schema, a, b := plan.SchemaFile(), plan.DataFile("a"), plan.DataFile("b")
	touch(t, dir, schema, a, b, "keep.txt", naming.NewPlan("other", nil).SchemaFile())
	var buf bytes.Buffer

	if err := removeStale(testLoggerAt(&buf, slog.LevelInfo), dir, "app", plan, map[string]bool{schema: true}); err != nil {
		t.Fatal(err)
	}
	// One line per removed file, in file name order; the rewritten schema file is not removed.
	want := "level=INFO msg=\"removed a file of an earlier dump\" database=app file=" + a + "\n" +
		"level=INFO msg=\"removed a file of an earlier dump\" database=app file=" + b + "\n"
	if buf.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", buf.String(), want)
	}
	for _, n := range []string{a, b} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s is logged but still there (err=%v)", n, err)
		}
	}
}

func TestRemoveStaleLogsNothingWhenNothingIsRemoved(t *testing.T) {
	dir := t.TempDir()
	plan := naming.NewPlan("app", nil)
	touch(t, dir, plan.SchemaFile())
	var buf bytes.Buffer
	if err := removeStale(testLoggerAt(&buf, slog.LevelDebug), dir, "app", plan, map[string]bool{plan.SchemaFile(): true}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected log:\n%s", buf.String())
	}
}

func TestSweepTempsLogsEachRemovedFileAtDebug(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, ".mysqldance-2.tmp", ".mysqldance-1.tmp", "app.schema.sql")
	var buf bytes.Buffer
	if err := sweepTemps(testLoggerAt(&buf, slog.LevelDebug), dir); err != nil {
		t.Fatal(err)
	}
	want := "level=DEBUG msg=\"removed a temporary file of an earlier run\" file=.mysqldance-1.tmp\n" +
		"level=DEBUG msg=\"removed a temporary file of an earlier run\" file=.mysqldance-2.tmp\n"
	if buf.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", buf.String(), want)
	}

	// Hidden at Info, and silent when there is nothing left to remove.
	touch(t, dir, ".mysqldance-3.tmp")
	buf.Reset()
	if err := sweepTemps(testLoggerAt(&buf, slog.LevelInfo), dir); err != nil {
		t.Fatal(err)
	}
	if err := sweepTemps(testLoggerAt(&buf, slog.LevelDebug), dir); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected log:\n%s", buf.String())
	}
}
