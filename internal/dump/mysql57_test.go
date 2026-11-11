package dump

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/sqlvalue"
)

// requireMySQL57 skips the test unless the test server is MySQL 5.7. These
// tests check what only 5.7 does; the rest of the suite runs on every server.
func (e *env) requireMySQL57(t *testing.T) {
	t.Helper()
	if !e.isMySQL57(t) {
		t.Skip("MySQL 5.7 only")
	}
}

// isMySQL57 reports whether the test server is MySQL 5.7. Tests use it only to
// leave out what 5.7 lacks (INVISIBLE columns); production code never asks.
func (e *env) isMySQL57(t *testing.T) bool {
	t.Helper()
	var v string
	if err := e.admin.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return strings.HasPrefix(v, "5.7.")
}

// TestMySQL57RoundTrip dumps a 5.7 database that has what 5.7 writes
// differently (int(11), a latin1 default character set, no CHECK or INVISIBLE),
// restores it into 5.7 and compares every row and value, then restores the same
// files into MySQL 8.4 (when MYSQLDANCE_TEST_RESTORE_DSN names one) and compares
// again.
func TestMySQL57RoundTrip(t *testing.T) {
	e := newEnv(t)
	e.requireMySQL57(t)
	db := uniqueName(t)
	// A latin1 database, as a stock 5.7 makes: the output keeps DEFAULT CHARSET=latin1.
	e.createDBWith(t, db, " CHARACTER SET latin1")
	e.exec(t,
		"CREATE TABLE md ("+
			" id INT NOT NULL PRIMARY KEY,"+
			" a VARCHAR(20) DEFAULT NULL,"+
			" b VARCHAR(40) NOT NULL DEFAULT 'it''s \\\\ ok',"+
			" u VARCHAR(40) CHARACTER SET utf8mb4 NULL,"+
			" bits BIT(5) NULL,"+
			" bl BLOB NULL,"+
			" j JSON NULL,"+
			" d3 DATETIME(3) NULL,"+
			" n TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),"+
			" st INT AS (id * 2) STORED,"+
			" vi INT AS (id + 1) VIRTUAL,"+
			" KEY ka (a(5)))",
		"INSERT INTO md (id, a, b, u, bits, bl, j, d3, n) VALUES"+
			" (1, 'x', 'line1\\nline2 \"q\"', '日本語 😀', b'10101', 0x00FF10, '{\"b\":  1, \"a\": [1,2]}', '2024-01-02 03:04:05.678', '2024-01-02 03:04:05.123456'),"+
			" (2, NULL, DEFAULT, NULL, NULL, '', NULL, NULL, '2038-01-19 03:14:07.000001')",
		"CREATE TABLE fk_parent (id INT PRIMARY KEY)",
		"CREATE TABLE fk_child (id INT PRIMARY KEY, p INT, FOREIGN KEY (p) REFERENCES fk_parent (id) ON DELETE CASCADE)",
		"INSERT INTO fk_parent VALUES (1), (2)",
		"INSERT INTO fk_child VALUES (1, 1), (2, 2)",
		"CREATE TABLE nokey (a INT, b VARCHAR(10))",
		"INSERT INTO nokey VALUES (1, 'x'), (1, 'x'), (NULL, NULL)",
		// a_top needs m_mid, which needs z_base: name order is not creation order.
		"CREATE VIEW z_base AS SELECT id, b FROM md",
		"CREATE VIEW m_mid AS SELECT id FROM z_base",
		"CREATE VIEW a_top AS SELECT m.id FROM m_mid m JOIN nokey k ON k.a = m.id",
	)

	dir := t.TempDir()
	e.dump(t, dir, db)
	schema := readFiles(t, dir)[naming.SchemaFile(db)]
	// The server's form is kept, not converted.
	for _, want := range []string{"`id` int(11) NOT NULL", "DEFAULT CHARSET=latin1"} {
		if !strings.Contains(schema, want) {
			t.Errorf("the schema lacks %q:\n%s", want, schema)
		}
	}

	before := e.contents(t, db)
	if got := before["VIEW m_mid"]; len(got) != 2 {
		t.Fatalf("m_mid rows = %q, want 2 rows", got)
	}
	e.roundTrip(t, dir, db)

	if second := restoreServer(t); second != nil {
		second.createdByDump(t, db)
		second.restore(t, dir)
		if after := second.contents(t, db); !reflect.DeepEqual(after, before) {
			t.Errorf("database %s differs after restoring into the second server\nbefore: %q\nafter:  %q", db, before, after)
		}
	}
}

// TestMySQL57ZeroDateDefault dumps a table whose TIMESTAMP NOT NULL column
// reports DEFAULT '0000-00-00 00:00:00' (the second one, with
// explicit_defaults_for_timestamp=OFF, the 5.7 default), restores it into 5.7
// and, when a second server is configured, into MySQL 8.4 whose default
// sql_mode has NO_ZERO_DATE, and compares every value.
func TestMySQL57ZeroDateDefault(t *testing.T) {
	e := newEnv(t)
	e.requireMySQL57(t)
	db := uniqueName(t)
	e.createDB(t, db)
	// Session settings of the admin connection, so the fixture does not depend
	// on the server's configuration; the dump reads with its own session.
	e.exec(t,
		"SET SESSION explicit_defaults_for_timestamp = OFF",
		"SET SESSION sql_mode = ''",
		"CREATE TABLE z (id INT NOT NULL PRIMARY KEY, a TIMESTAMP NOT NULL, b TIMESTAMP NOT NULL)",
		"INSERT INTO z (id, a) VALUES (1, '2024-01-02 03:04:05'), (2, '2024-06-07 08:09:10')",
	)

	dir := t.TempDir()
	e.dump(t, dir, db)
	schema := readFiles(t, dir)[naming.SchemaFile(db)]
	if !strings.Contains(schema, "DEFAULT '0000-00-00 00:00:00'") {
		t.Fatalf("the fixture has no zero-date default; the test checks nothing:\n%s", schema)
	}

	before := e.contents(t, db)
	// b was left out of the INSERT, so it holds the zero date; make sure the
	// comparison below sees that value and not a NULL or an empty string.
	for _, row := range before["BASE TABLE z"] {
		if !strings.Contains(row, "0000-00-00 00:00:00") {
			t.Fatalf("a row of z has no zero date in b: %q", row)
		}
	}
	if len(before["BASE TABLE z"]) != 2 {
		t.Fatalf("z rows = %q, want 2 rows", before["BASE TABLE z"])
	}
	e.roundTrip(t, dir, db)

	if second := restoreServer(t); second != nil {
		// Precondition: under the server's own sql_mode the default is refused, so
		// it is the file header's SET SQL_MODE that lets the restore through.
		second.createDB(t, db+"_probe")
		// The probe database is a scratch one: the admin connection's USE moving to
		// it is harmless, because contents and restore name the database.
		_, err := second.admin.ExecContext(context.Background(),
			"CREATE TABLE p (b TIMESTAMP NOT NULL DEFAULT '0000-00-00 00:00:00')")
		if err == nil || !strings.Contains(err.Error(), "Invalid default value") {
			t.Fatalf("want 'Invalid default value' from the second server under its own sql_mode, got %v; the test checks nothing", err)
		}
		second.createdByDump(t, db)
		second.restore(t, dir)
		if after := second.contents(t, db); !reflect.DeepEqual(after, before) {
			t.Errorf("database %s differs after restoring into the second server\nbefore: %q\nafter:  %q", db, before, after)
		}
	}
}

// restoreServer returns the env of the second server, or nil when none is
// configured (MYSQLDANCE_TEST_RESTORE_DSN is empty).
func restoreServer(t *testing.T) *env {
	t.Helper()
	if os.Getenv("MYSQLDANCE_TEST_RESTORE_DSN") == "" {
		t.Log("MYSQLDANCE_TEST_RESTORE_DSN is not set: the dump is not restored into a second server")
		return nil
	}
	return newRestoreEnv(t)
}

// createdByDump drops database name at cleanup: the restored files create it.
func (e *env) createdByDump(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := e.admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+sqlvalue.QuoteIdent(name)); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
}

// mysql57DefaultsFixture creates a table with the defaults 5.7 reports without
// DEFAULT_GENERATED, and a foreign key that names no ON UPDATE / ON DELETE.
func mysql57DefaultsFixture(t *testing.T, e *env) string {
	t.Helper()
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE d ("+
			" id INT NOT NULL PRIMARY KEY,"+
			" ts TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,"+
			" dt6 DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),"+
			" s VARCHAR(30) NOT NULL DEFAULT 'CURRENT_TIMESTAMP',"+
			" c CHAR(20) NOT NULL DEFAULT 'CURRENT_TIMESTAMP(6)')",
		"CREATE TABLE child (id INT PRIMARY KEY, d_id INT, FOREIGN KEY (d_id) REFERENCES d (id))",
	)
	return db
}

var timestampText = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`)

// TestMySQL57DefaultsInSQLiteOutput: DEFAULT CURRENT_TIMESTAMP is an expression
// as on 8.4 (a row that takes it gets a time), while a string column holding the
// text CURRENT_TIMESTAMP keeps a string. The foreign key shows 5.7's RESTRICT.
func TestMySQL57DefaultsInSQLiteOutput(t *testing.T) {
	e := newEnv(t)
	e.requireMySQL57(t)
	db := mysql57DefaultsFixture(t, e)
	dir := t.TempDir()
	e.dumpSQLite(t, dir, datafile.Options{}, db)

	schema := readFiles(t, dir)[naming.SchemaFile(db)]
	if n := strings.Count(schema, "DEFAULT CURRENT_TIMESTAMP"); n != 2 {
		t.Errorf("the schema has %d DEFAULT CURRENT_TIMESTAMP, want 2 (ts and dt6):\n%s", n, schema)
	}
	for _, want := range []string{"`s` TEXT NOT NULL DEFAULT 'CURRENT_TIMESTAMP'", "`c` TEXT NOT NULL DEFAULT 'CURRENT_TIMESTAMP(6)'"} {
		if !strings.Contains(schema, want) {
			t.Errorf("the schema lacks %q:\n%s", want, schema)
		}
	}
	// 5.7 reports RESTRICT where 8.x reports NO ACTION; the server's word is output.
	if !strings.Contains(schema, "ON DELETE RESTRICT ON UPDATE RESTRICT") {
		t.Errorf("the schema lacks ON DELETE RESTRICT ON UPDATE RESTRICT:\n%s", schema)
	}

	lite := restoreSQLite(t, dir)
	if _, err := lite.Exec("INSERT INTO d (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"ts", "dt6"} {
		got := cells(t, lite, "SELECT "+col+" FROM d")
		if len(got) != 1 || !timestampText.MatchString(strings.Trim(got[0], `"`)) {
			t.Errorf("%s of a row taking the defaults = %q, want a time", col, got)
		}
	}
	want := []string{`"CURRENT_TIMESTAMP","CURRENT_TIMESTAMP(6)"`}
	if got := cells(t, lite, "SELECT s, c FROM d"); !reflect.DeepEqual(got, want) {
		t.Errorf("string defaults = %q, want %q", got, want)
	}
}

// TestMySQL57DefaultsInPostgresOutput is the same for PostgreSQL.
func TestMySQL57DefaultsInPostgresOutput(t *testing.T) {
	e := newEnv(t)
	e.requireMySQL57(t)
	pg := newPGEnv(t)
	db := mysql57DefaultsFixture(t, e)
	dir := t.TempDir()
	log := e.dumpPostgres(t, dir, datafile.Options{}, db)
	if strings.Contains(log, "default expression") {
		t.Errorf("the log warns about a default expression:\n%s", log)
	}
	// The foreign keys are in the constraints file.
	constraints := readFiles(t, dir)[naming.ConstraintsFile(db)]
	if !strings.Contains(constraints, "ON DELETE RESTRICT ON UPDATE RESTRICT") {
		t.Errorf("the constraints file lacks ON DELETE RESTRICT ON UPDATE RESTRICT:\n%s", constraints)
	}
	pg.restore(t, dir)

	conn := pg.connect(t)
	if _, err := conn.Exec(context.Background(),
		"INSERT INTO "+pgx.Identifier{db, "d"}.Sanitize()+" (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	got := pgCells(t, conn, db, "SELECT id::text, (ts IS NOT NULL)::text, (dt6 IS NOT NULL)::text, s, c FROM d")
	want := []string{`"1","true","true","CURRENT_TIMESTAMP","CURRENT_TIMESTAMP(6)"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defaults = %q, want %q", got, want)
	}
}
