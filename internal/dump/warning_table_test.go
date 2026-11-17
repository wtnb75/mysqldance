package dump

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/writer"
)

// A warning about a table carries the table as an attribute, so a log can be
// filtered by it; one that concerns no table (a view) has no table attribute
// (B-090). The database attribute is on both.
func TestLogWarningTableAttribute(t *testing.T) {
	var log bytes.Buffer
	logWarning(testLogger(&log), "d", writer.Warning{Table: "t", Message: "about a table"})
	logWarning(testLogger(&log), "d", writer.Warning{Message: "about a view"})
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log = %q, want two lines", log.String())
	}
	if want := `level=WARN msg="about a table" database=d table=t`; lines[0] != want {
		t.Errorf("table warning = %q, want %q", lines[0], want)
	}
	if want := `level=WARN msg="about a view" database=d`; lines[1] != want {
		t.Errorf("warning without a table = %q, want %q", lines[1], want)
	}
}

// The warnings of a writer and of the reader both reach the log with table.
func TestWarningsCarryTable(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY, body TEXT, FULLTEXT KEY ft (body))")
	var log bytes.Buffer
	err := Run(context.Background(), Options{
		Source: e.cfg, DBs: []string{db}, OutDir: t.TempDir(), Log: testLogger(&log), Writer: writer.NewSQLite(datafile.Options{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for line := range strings.SplitSeq(log.String(), "\n") {
		if strings.Contains(line, "index `ft` (FULLTEXT) is not output") {
			found = true
			if !strings.Contains(line, "database="+db) || !strings.Contains(line, "table=a") {
				t.Errorf("writer warning lacks database or table: %s", line)
			}
		}
	}
	if !found {
		t.Errorf("no warning about the FULLTEXT index:\n%s", log.String())
	}
}
