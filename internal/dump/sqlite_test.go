package dump

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/writer"
)

// dumpSQLite dumps dbs with the SQLite writer and returns what was logged.
func (e *env) dumpSQLite(t *testing.T, dir string, data datafile.Options, dbs ...string) string {
	t.Helper()
	var log bytes.Buffer
	err := Run(context.Background(), Options{
		Source: e.cfg, DBs: dbs, OutDir: dir, Log: testLogger(&log), Writer: writer.NewSQLite(data),
	})
	if err != nil {
		t.Fatalf("Run(%v): %v", dbs, err)
	}
	return log.String()
}

// restoreSQLite runs every *.sql file of dir, in file-name order, against one
// in-memory SQLite database, like `cat dir/*.sql | sqlite3`.
func restoreSQLite(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range sqlFiles(t, dir) {
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(text)); err != nil {
			t.Fatalf("SQLite rejected %s: %v", name, err)
		}
	}
	return db
}

// cells runs query and renders every cell as text, NULL as "NULL", so that the
// rows of MySQL and of SQLite can be compared.
func cells(t *testing.T, q interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string) []string {
	t.Helper()
	rows, err := q.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		line := make([]string, len(vals))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				line[i] = "NULL"
			case []byte:
				line[i] = strconv.Quote(string(x))
			case string:
				line[i] = strconv.Quote(x)
			case float64:
				line[i] = strconv.Quote(strconv.FormatFloat(x, 'g', -1, 64))
			default:
				line[i] = strconv.Quote(fmt.Sprint(x))
			}
		}
		out = append(out, strings.Join(line, ","))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// adminQuery adapts the pinned administrative connection to cells.
type adminQuery struct{ e *env }

func (a adminQuery) Query(q string, args ...any) (*sql.Rows, error) {
	return a.e.admin.QueryContext(context.Background(), q, args...)
}

func sqliteFixture(t *testing.T, e *env) string {
	t.Helper()
	e.skipOnMySQL56(t, "JSON type or generated columns, which this fixture has")
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE parent (id BIGINT UNSIGNED NOT NULL PRIMARY KEY, code VARCHAR(10) NOT NULL, UNIQUE KEY uk_code (code))",
		"CREATE TABLE child ("+
			" id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,"+
			" parent_id BIGINT UNSIGNED NULL,"+
			" qty DECIMAL(10,2) NOT NULL DEFAULT 1.50,"+
			" flags BIT(64) NULL, small BIT(8) NULL, data BLOB NULL, f DOUBLE NULL, d DATE NULL,"+
			" dt DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, j JSON NULL, note TEXT NULL,"+
			" KEY idx_d (d, id DESC),"+
			" CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parent (id) ON DELETE CASCADE)",
		"CREATE TABLE nokey (a INT, b VARCHAR(10))",
		"CREATE TABLE gen (id INT PRIMARY KEY, x INT, y INT AS (x * 2) STORED)",
		"CREATE TABLE empty_t (id INT PRIMARY KEY)",
		"CREATE VIEW v AS SELECT id, IF(parent_id IS NULL, 'orphan', 'owned') AS kind, CONCAT(note, '!') AS shout FROM child WHERE id > 0",
		// DATE_FORMAT has no SQLite equivalent: the view is reported, not output.
		"CREATE VIEW v_year AS SELECT DATE_FORMAT(d, '%Y') AS y FROM child",
		// It reads a view that is not output, so it is not output either.
		"CREATE VIEW v_year2 AS SELECT y FROM v_year",

		"INSERT INTO parent VALUES (18446744073709551615, 'max'), (1, 'one')",
		"INSERT INTO child (parent_id, qty, flags, small, data, f, d, dt, j, note) VALUES"+
			" (18446744073709551615, 1.10, X'FFFFFFFFFFFFFFFF', b'10101', X'00FF41', 1.5, '2024-02-29', '2024-02-29 01:02:03', '{\"a\": 1}', 'line1\nline2\r\nit''s \\\\ 日本語 😀'),"+
			" (1, -0.05, b'1', 0, '', -2.25, '1000-01-01','2000-01-01 00:00:00', 'null', ''),"+
			" (NULL, DEFAULT, NULL, NULL, NULL, NULL, NULL, '2001-01-01 00:00:00', NULL, NULL)",
		"INSERT INTO nokey VALUES (1, 'x'), (1, 'x'), (NULL, NULL), (0, '')",
		"INSERT INTO gen (id, x) VALUES (1, 2), (2, NULL)",
	)
	return db
}

// The acceptance test: the SQLite files restore into a real SQLite and the row
// counts and the values match the source.
func TestSQLiteRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := sqliteFixture(t, e)
	dir := t.TempDir()
	log := e.dumpSQLite(t, dir, datafile.Options{}, db)

	// Nothing that SQLite cannot hold is dropped without a word.
	for _, want := range []string{
		"view `v_year` is not output: DATE_FORMAT() cannot be converted to SQLite",
		"view `v_year2` is not output: it reads the view `v_year`, which is not output",
		"view `v` is output, but its result may differ from MySQL",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if got := sqlFiles(t, dir); len(got) != 6 {
		t.Errorf("files = %v, want the schema and 5 data files", got)
	}
	if schema := readFiles(t, dir)[naming.SchemaFile(db)]; strings.Contains(schema, "CREATE DATABASE") || strings.Contains(schema, "USE ") {
		t.Errorf("the schema file has MySQL statements:\n%s", schema)
	}

	lite := restoreSQLite(t, dir)
	compareSQLiteRows(t, e, db, lite)

	// Constraints and indexes came along.
	if got := cells(t, lite, "SELECT `table`, `from`, `to`, on_delete FROM pragma_foreign_key_list('child')"); !reflect.DeepEqual(got,
		[]string{`"parent","parent_id","id","CASCADE"`}) {
		t.Errorf("foreign keys = %q", got)
	}
	if got := cells(t, lite, "SELECT name, `unique` FROM pragma_index_list('parent') WHERE origin = 'c' ORDER BY name"); !reflect.DeepEqual(got,
		[]string{`"parent__uk_code","1"`}) {
		t.Errorf("unique indexes of parent = %q", got)
	}
	// fk_parent is the index MySQL created for the foreign key.
	if got := cells(t, lite, "SELECT name FROM pragma_index_list('child') WHERE origin = 'c' ORDER BY name"); !reflect.DeepEqual(got,
		[]string{`"child__fk_parent"`, `"child__idx_d"`}) {
		t.Errorf("indexes of child = %q", got)
	}
	if _, err := lite.Exec("INSERT INTO parent VALUES ('2', 'one')"); err == nil {
		t.Error("SQLite accepted a duplicate of a UNIQUE key")
	}
	// Only the view that converts is restored, and it returns the MySQL rows.
	if got := cells(t, lite, "SELECT name FROM sqlite_master WHERE type = 'view'"); !reflect.DeepEqual(got, []string{`"v"`}) {
		t.Errorf("views = %q, want only v", got)
	}
	want := cells(t, adminQuery{e}, "SELECT id, kind, shout FROM `"+db+"`.v ORDER BY id")
	if got := cells(t, lite, "SELECT id, kind, shout FROM v ORDER BY id"); !reflect.DeepEqual(got, want) {
		t.Errorf("view v in SQLite = %q, in MySQL = %q", got, want)
	}
}

// compareSQLiteRows checks that every table of the sqliteFixture database db
// holds the same rows in lite, a restored SQLite database, as in MySQL.
func compareSQLiteRows(t *testing.T, e *env, db string, lite interface {
	Query(string, ...any) (*sql.Rows, error)
}) {
	t.Helper()
	my := adminQuery{e}
	qualified := func(table string) string { return "`" + db + "`.`" + table + "`" }
	for _, c := range []struct{ name, mysql, sqlite string }{
		{"parent", "SELECT id, code FROM " + qualified("parent") + " ORDER BY id", "SELECT id, code FROM parent ORDER BY id"},
		{"child",
			"SELECT id, parent_id, qty, flags+0, small+0, data, f, d, dt, j, note FROM " + qualified("child") + " ORDER BY id",
			"SELECT id, parent_id, qty, flags, small, data, f, d, dt, j, note FROM child ORDER BY id"},
		{"nokey", "SELECT a, b FROM " + qualified("nokey") + " ORDER BY a, b", "SELECT a, b FROM nokey ORDER BY a, b"},
		// The generated column y is not output; the stored column x is.
		{"gen", "SELECT id, x FROM " + qualified("gen") + " ORDER BY id", "SELECT id, x FROM gen ORDER BY id"},
		{"empty_t", "SELECT id FROM " + qualified("empty_t"), "SELECT id FROM empty_t"},
	} {
		want := cells(t, my, c.mysql)
		got := cells(t, lite, c.sqlite)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("table %s differs\nMySQL:  %q\nSQLite: %q", c.name, want, got)
		}
		if len(got) == 0 && c.name != "empty_t" {
			t.Errorf("table %s restored no rows", c.name)
		}
	}
}

// The same data gives the same bytes, and one changed row changes one line.
func TestSQLiteDeterministicAndMinimalDiff(t *testing.T) {
	e := newEnv(t)
	db := sqliteFixture(t, e)
	opt := datafile.Options{RowsPerInsert: 2}
	first, second := t.TempDir(), t.TempDir()
	e.dumpSQLite(t, first, opt, db)
	e.dumpSQLite(t, second, opt, db)
	a, b := readFiles(t, first), readFiles(t, second)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two dumps of the same data differ:\n%q\n%q", a, b)
	}

	e.exec(t, "UPDATE child SET note = 'changed' WHERE id = 2")
	third := t.TempDir()
	e.dumpSQLite(t, third, opt, db)
	c := readFiles(t, third)
	name := naming.DataFile(db, "child")
	for f, content := range a {
		if f == name {
			continue
		}
		if c[f] != content {
			t.Errorf("file %s changed although only child did", f)
		}
	}
	if del, add := lineDiff(a[name], c[name]); del != 1 || add != 1 {
		t.Errorf("one updated row changed %d lines deleted, %d added; want 1 and 1", del, add)
	}
}

// dumpSplitBigTable dumps a 300-row table as SQLite files split over several
// files and INSERT statements, and returns the output directory.
func dumpSplitBigTable(t *testing.T, e *env) string {
	t.Helper()
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE big (id INT PRIMARY KEY, s VARCHAR(20))")
	var vals []string
	for i := 1; i <= 300; i++ {
		vals = append(vals, fmt.Sprintf("(%d, 'row %d')", i, i))
	}
	e.exec(t, "INSERT INTO big VALUES "+strings.Join(vals, ","))
	dir := t.TempDir()
	e.dumpSQLite(t, dir, datafile.Options{RowsPerInsert: 20, RowsPerFile: 50}, db)
	if n := len(sqlFiles(t, dir)); n < 3 {
		t.Fatalf("got %d files, want the table split into several", n)
	}
	return dir
}

func TestSQLiteSplitsLargeTable(t *testing.T) {
	lite := restoreSQLite(t, dumpSplitBigTable(t, newEnv(t)))
	if got := cells(t, lite, "SELECT count(*), sum(id) FROM big"); !reflect.DeepEqual(got, []string{`"300","45150"`}) {
		t.Errorf("restored %q", got)
	}
}
