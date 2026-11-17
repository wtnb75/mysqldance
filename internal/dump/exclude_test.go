package dump

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/naming"
)

func (e *env) dumpExcluding(t *testing.T, dir string, db string, exclude ...string) string {
	t.Helper()
	var log bytes.Buffer
	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log), ExcludeTables: exclude})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return log.String()
}

func TestExcludeTable(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE keep (id INT PRIMARY KEY)",
		"CREATE TABLE sessions (id INT PRIMARY KEY)",
		"INSERT INTO keep VALUES (1)", "INSERT INTO sessions VALUES (1)",
		"CREATE TRIGGER sessions_bi BEFORE INSERT ON sessions FOR EACH ROW SET @x = 1",
	)
	full := t.TempDir()
	e.dump(t, full, db)

	dir := t.TempDir()
	// Data of an earlier dump that included the table.
	e.dump(t, dir, db)
	if !exists(t, filepath.Join(dir, naming.DataFile(db, "sessions"))) {
		t.Fatal("the data file of sessions was not written by the full dump")
	}
	e.dumpExcluding(t, dir, db, db+".sessions")

	files := readFiles(t, dir)
	for name, text := range files {
		if strings.Contains(name, "sessions") || strings.Contains(text, "sessions") {
			t.Errorf("%s still mentions sessions:\n%.300s", name, text)
		}
	}
	if _, ok := files[naming.DataFile(db, "keep")]; !ok {
		t.Errorf("the data file of keep is missing: %v", files)
	}
	// The earlier file of the table is removed like any file not written again.
	if _, ok := files[naming.TriggersFile(db)]; ok {
		t.Errorf("a triggers file was written although its only trigger is on the excluded table")
	}

	// A run that excludes nothing is byte-identical to one without the option.
	again := t.TempDir()
	e.dumpExcluding(t, again, db)
	if a, b := readFiles(t, again), readFiles(t, full); !reflect.DeepEqual(a, b) {
		t.Error("a dump with an empty exclusion differs from the plain dump")
	}
}

func TestExcludeTableBadSpecTouchesNothing(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY)", "CREATE VIEW v AS SELECT id FROM t")
	for _, spec := range []string{db + ".nope", db + ".v", "other_db.t"} {
		dir := filepath.Join(t.TempDir(), "out")
		err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, ExcludeTables: []string{spec}})
		if err == nil || !strings.Contains(err.Error(), spec) {
			t.Errorf("exclude %q: err = %v, want an error naming it", spec, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("exclude %q: the output directory was created (stat err = %v)", spec, err)
		}
	}
}

func TestExcludeTableWarnsAboutReferrers(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE parent (id INT PRIMARY KEY)",
		"CREATE TABLE child (id INT PRIMARY KEY, pid INT, CONSTRAINT child_fk FOREIGN KEY (pid) REFERENCES parent (id))",
		"CREATE VIEW v_parent AS SELECT id FROM parent",
	)
	log := e.dumpExcluding(t, t.TempDir(), db, db+".parent")
	for _, want := range []string{
		"level=WARN", "foreign key `child_fk` of table `child` references table", "view `v_parent` reads table `parent`", "database=" + db,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// The foreign key warning is about the table that has it; the view's is not about a table (B-090).
	for line := range strings.SplitSeq(log, "\n") {
		switch {
		case strings.Contains(line, "foreign key `child_fk`") && !strings.Contains(line, "table=child"):
			t.Errorf("foreign key warning lacks table=child: %s", line)
		case strings.Contains(line, "view `v_parent`") && strings.Contains(line, "table="):
			t.Errorf("view warning has a table attribute: %s", line)
		}
	}
}
