package dump

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/writer"
)

// Tests that the output and the warnings do not depend on Source.Jobs: the same
// databases dumped with 1, 2 and 8 readers give the same files, byte for byte,
// and the same log, line for line.

// splitData makes the big table below span several data files.
var splitData = datafile.Options{RowsPerInsert: 10, MaxInsertBytes: 1 << 20, RowsPerFile: 20}

// jobsFixture creates two databases that between them hold every shape the
// parallel path handles: keyed and keyless tables, a table split into several
// files, and views (which the SQLite and PostgreSQL writers warn about).
func (e *env) jobsFixture(t *testing.T) []string {
	t.Helper()
	var dbs []string
	for range 2 {
		// createDB selects the database on e's single pinned connection (not a
		// pool), so the unqualified DDL below goes to this database.
		db := e.manyTables(t, 6)
		var values []string
		for i := 1; i <= 100; i++ {
			values = append(values, fmt.Sprintf("(%d, 'row %d')", i, i))
		}
		e.exec(t,
			"CREATE TABLE nokey (a INT, b INT)",
			"INSERT INTO nokey VALUES (2, 1), (1, 2), (1, 1)",
			"CREATE TABLE big (id INT PRIMARY KEY, v VARCHAR(20))",
			"INSERT INTO big VALUES "+strings.Join(values, ", "),
			"CREATE VIEW v1 AS SELECT id FROM t01",
			"CREATE VIEW v2 AS SELECT id FROM t02",
		)
		dbs = append(dbs, db)
	}
	return dbs
}

func TestJobsOutputAndLogIndependentOfJobs(t *testing.T) {
	e := newEnv(t)
	dbs := e.jobsFixture(t)

	writers := []struct {
		name string
		make func() writer.Writer
		// warns is true when the writer reports what it cannot carry over, so
		// the log is not empty and its order can be compared.
		warns bool
	}{
		{"mysql", func() writer.Writer { return writer.NewMySQL(splitData) }, false},
		{"sqlite", func() writer.Writer { return writer.NewSQLite(splitData) }, true},
		{"postgres", func() writer.Writer { return writer.NewPostgres(splitData) }, true},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			run := func(jobs int) (map[string]string, string) {
				dir := t.TempDir()
				cfg := e.cfg
				cfg.Jobs = jobs
				var log bytes.Buffer
				opt := Options{Source: cfg, DBs: dbs, OutDir: dir, Writer: w.make(), Log: &log}
				if err := Run(context.Background(), opt); err != nil {
					t.Fatalf("Run(jobs=%d): %v", jobs, err)
				}
				return readFiles(t, dir), log.String()
			}

			wantFiles, wantLog := run(1)
			// Every table has a data file, and big is split into several.
			for _, db := range dbs {
				// A split table's parts are named "<data file name without .sql>.NNNN.sql".
				parts := strings.TrimSuffix(naming.DataFile(db, "big"), ".sql") + "."
				if n := countFiles(wantFiles, parts); n < 3 {
					t.Fatalf("%d data files of %s.big, want it split into several: %v", n, db, slices.Sorted(maps.Keys(wantFiles)))
				}
				for _, tbl := range []string{"t01", "t06", "nokey"} {
					if _, ok := wantFiles[naming.DataFile(db, tbl)]; !ok {
						t.Fatalf("no data file of %s.%s: %v", db, tbl, slices.Sorted(maps.Keys(wantFiles)))
					}
				}
			}
			if w.warns {
				// Both views of both databases are reported, so the order of the
				// warnings has something to compare.
				for _, db := range dbs {
					for _, v := range []string{"`v1`", "`v2`"} {
						if !hasWarning(wantLog, fmt.Sprintf("database %q", db), v) {
							t.Fatalf("serial log has no warning for view %s of %q:\n%s", v, db, wantLog)
						}
					}
				}
			}

			// Parallel runs are repeated: a difference caused by which table
			// finishes first would not show up every time.
			for _, jobs := range []int{2, 8} {
				for range 3 {
					files, log := run(jobs)
					if !reflect.DeepEqual(files, wantFiles) {
						t.Errorf("files with %d jobs differ from the serial ones (%s)", jobs, diffNames(wantFiles, files))
					}
					if log != wantLog {
						t.Errorf("log with %d jobs differs from the serial one:\nserial:\n%s\nparallel:\n%s", jobs, wantLog, log)
					}
				}
			}
		})
	}
}

// countFiles counts the files whose name starts with prefix.
func countFiles(files map[string]string, prefix string) int {
	n := 0
	for name := range files {
		if strings.HasPrefix(name, prefix) {
			n++
		}
	}
	return n
}

// hasWarning reports whether a line of log contains both of the given strings.
func hasWarning(log string, parts ...string) bool {
	for line := range strings.SplitSeq(log, "\n") {
		if !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(line, p) }) {
			return true
		}
	}
	return false
}

// diffNames says which files are missing, extra or different.
func diffNames(want, got map[string]string) string {
	var out []string
	for n, w := range want {
		if g, ok := got[n]; !ok {
			out = append(out, "missing "+n)
		} else if g != w {
			out = append(out, "different "+n)
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			out = append(out, "extra "+n)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}
