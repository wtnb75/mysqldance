package dump

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mysqldance/internal/naming"
)

// Tests of the progress records: the start and end of a database and the rows,
// bytes and time of each table (B-053).

// logAttr returns the value of the attribute key on a text log line, or "".
func logAttr(line, key string) string {
	for f := range strings.FieldsSeq(line) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}

func attrInt(t *testing.T, line, key string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(logAttr(line, key), 10, 64)
	if err != nil {
		t.Fatalf("attribute %s of %q: %v", key, line, err)
	}
	return n
}

// runProgress dumps dbs with the given number of jobs at the Debug level, so that
// the log of the statements is checked too, and returns the log and the output
// directory.
func (e *env) runProgress(t *testing.T, jobs int, dbs ...string) (log, dir string) {
	t.Helper()
	cfg := e.cfg
	cfg.Jobs = jobs
	var buf bytes.Buffer
	dir = t.TempDir()
	opt := Options{Source: cfg, DBs: dbs, OutDir: dir, Log: connLogger(&buf)}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run(jobs=%d): %v\n%s", jobs, err, buf.String())
	}
	return buf.String(), dir
}

func TestProgressLog(t *testing.T) {
	const secret = "s3cr3t-row-value"
	for _, jobs := range []int{1, 3} {
		t.Run("jobs="+strconv.Itoa(jobs), func(t *testing.T) {
			e := newEnv(t)
			db := uniqueName(t)
			e.createDB(t, db)
			e.exec(t,
				"CREATE TABLE a (id INT PRIMARY KEY, s VARCHAR(40))",
				"INSERT INTO a VALUES (1, '"+secret+"'), (2, 'x'), (3, 'y')",
				"CREATE TABLE b (id INT PRIMARY KEY)",
				"CREATE TABLE c (id INT PRIMARY KEY)", "INSERT INTO c VALUES (7)",
			)
			log, dir := e.runProgress(t, jobs, db)
			e.requireNoSecret(t, log)
			if strings.Contains(log, secret) {
				t.Errorf("log contains a row value:\n%s", log)
			}

			plan := naming.NewPlan(db, []string{"a", "b", "c"})
			wantRows := map[string]int64{"a": 3, "b": 0, "c": 1}
			var rows, size int64
			for _, tbl := range []string{"a", "b", "c"} {
				line := logLine(log, "level=INFO", "msg=\"table dumped\"", "database="+db, "table="+tbl+" ")
				if line == "" {
					t.Fatalf("no Info line \"table dumped\" for table %s:\n%s", tbl, log)
				}
				if got := attrInt(t, line, "rows"); got != wantRows[tbl] {
					t.Errorf("table %s: rows = %d, want %d", tbl, got, wantRows[tbl])
				}
				fi, err := os.Stat(filepath.Join(dir, plan.DataFile(tbl)))
				if err != nil {
					t.Fatal(err)
				}
				if got := attrInt(t, line, "bytes"); got != fi.Size() {
					t.Errorf("table %s: bytes = %d, want the file size %d", tbl, got, fi.Size())
				}
				if got := attrInt(t, line, "files"); got != 1 {
					t.Errorf("table %s: files = %d, want 1", tbl, got)
				}
				if logAttr(line, "duration") == "" {
					t.Errorf("table %s: no duration in %q", tbl, line)
				}
				rows += wantRows[tbl]
				size += fi.Size()
			}

			start := logLine(log, "level=INFO", "msg=\"dumping database\"", "database="+db)
			end := logLine(log, "level=INFO", "msg=\"database dumped\"", "database="+db)
			if start == "" || end == "" {
				t.Fatalf("want a start and an end line of the database:\n%s", log)
			}
			if strings.Index(log, start) > strings.Index(log, end) {
				t.Errorf("the start line comes after the end line:\n%s", log)
			}
			for key, want := range map[string]int64{"tables": 3, "rows": rows, "bytes": size, "files": 3} {
				if got := attrInt(t, end, key); got != want {
					t.Errorf("database line: %s = %d, want %d\n%s", key, got, want, end)
				}
			}
			if logAttr(end, "duration") == "" {
				t.Errorf("no duration in %q", end)
			}
		})
	}
}

// A failed run says nothing of the database being done.
func TestProgressLogNotDoneOnFailure(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	var buf bytes.Buffer
	dir := t.TempDir()
	failOn(t, filepath.Join(dir, naming.DataFile(db, "b")), nil)
	opt := Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: connLogger(&buf)}
	if err := Run(context.Background(), opt); err == nil {
		t.Fatal("Run with an injected write failure succeeded")
	}
	if !strings.Contains(buf.String(), "dumping database") {
		t.Errorf("the failed run did not even log the start:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "database dumped") {
		t.Errorf("a failed run logged the end of the database:\n%s", buf.String())
	}
}
