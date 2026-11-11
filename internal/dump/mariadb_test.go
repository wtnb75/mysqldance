package dump

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
)

// requireMariaDB skips the test unless the test server is MariaDB. These tests
// check what only MariaDB does; the rest of the suite runs on every server.
func (e *env) requireMariaDB(t *testing.T) {
	t.Helper()
	var v string
	if err := e.admin.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(v), "mariadb") {
		t.Skip("MariaDB only")
	}
}

// TestMariaDBRoundTrip dumps a MariaDB database that uses what MariaDB writes
// differently (DEFAULT NULL, current_timestamp(), expression defaults, JSON as
// LONGTEXT with a CHECK, the UUID and INET6 types, INVISIBLE and generated
// columns), restores it into MariaDB and compares every row and value.
func TestMariaDBRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.requireMariaDB(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE md ("+
			" id INT NOT NULL PRIMARY KEY,"+
			" a VARCHAR(20) DEFAULT NULL,"+
			" b VARCHAR(20) NOT NULL DEFAULT 'it''s \\\\ ok',"+
			" c INT DEFAULT (id + 1),"+
			" n TIMESTAMP NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),"+
			" j JSON NULL,"+
			" u UUID NULL,"+
			" ip INET6 NULL,"+
			" hid INT INVISIBLE,"+
			" st INT AS (id * 2) STORED,"+
			" vi INT AS (id + 1) VIRTUAL,"+
			" CHECK (id > 0))",
		"INSERT INTO md (id, a, b, c, n, j, u, ip, hid) VALUES"+
			" (1, 'x', 'plain', 7, '2024-01-02 03:04:05', '{\"b\":  1, \"a\": [1,2]}', '123e4567-e89b-12d3-a456-426614174000', '::1', 9),"+
			" (2, NULL, DEFAULT, NULL, '2038-01-19 03:14:07', NULL, NULL, NULL, NULL)",
		"INSERT INTO md (id) VALUES (3)",
		"CREATE TABLE nokey (a INT, b VARCHAR(10))",
		"INSERT INTO nokey VALUES (1, 'x'), (1, 'x'), (NULL, NULL)",
		// a_top needs z_base: name order is not creation order.
		"CREATE VIEW z_base AS SELECT id, b FROM md",
		"CREATE VIEW m_mid AS SELECT id FROM z_base",
		"CREATE VIEW a_top AS SELECT m.id FROM m_mid m JOIN nokey k ON k.a = m.id",
	)

	dir := t.TempDir()
	e.dump(t, dir, db)
	e.roundTrip(t, dir, db)
	after := e.contents(t, db)
	if got := after["VIEW m_mid"]; len(got) != 3 {
		t.Errorf("m_mid rows = %q, want 3 rows", got)
	}
}

// TestUnsupportedTableTypeKeepsPreviousDump: a SEQUENCE (or a SYSTEM VERSIONED
// table) stops the run during the inspection, before any output file is touched.
func TestUnsupportedTableTypeKeepsPreviousDump(t *testing.T) {
	for _, tc := range []struct{ name, create, typ string }{
		{"seq", "CREATE SEQUENCE seq", "SEQUENCE"},
		{"hist", "CREATE TABLE hist (id INT PRIMARY KEY) WITH SYSTEM VERSIONING", "SYSTEM VERSIONED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.requireMariaDB(t)
			db := uniqueName(t)
			e.createDB(t, db)
			e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1)")
			dir := t.TempDir()
			e.dump(t, dir, db)
			before := readFiles(t, dir)

			e.exec(t, tc.create, "INSERT INTO t VALUES (2)")
			var log bytes.Buffer
			err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log)})
			if err == nil {
				t.Fatalf("Run succeeded with a %s in the database", tc.typ)
			}
			if !strings.Contains(err.Error(), "`"+tc.name+"`") || !strings.Contains(err.Error(), tc.typ) {
				t.Errorf("error = %q, want the name %s and the type %s", err, tc.name, tc.typ)
			}
			if after := readFiles(t, dir); !reflect.DeepEqual(after, before) {
				t.Errorf("the previous dump changed:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// mariadbDefaultsFixture creates a table whose defaults MariaDB reports as the
// text of the DEFAULT clause. The table is empty: the test inserts a row that
// takes every default.
func mariadbDefaultsFixture(t *testing.T, e *env) string {
	t.Helper()
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE d ("+
			" id INT NOT NULL PRIMARY KEY,"+
			" a VARCHAR(20) DEFAULT NULL,"+
			" b VARCHAR(20) DEFAULT 'x',"+
			" c VARCHAR(20) DEFAULT 'a\\\\b',"+
			" e VARCHAR(20) DEFAULT '',"+
			" f VARCHAR(20) DEFAULT 'NULL',"+
			" g VARCHAR(20) DEFAULT 'it''s',"+
			" h INT DEFAULT -5,"+
			" i DECIMAL(5,2) DEFAULT -1.50,"+
			" m DATE DEFAULT '2020-01-02',"+
			" n TIMESTAMP NOT NULL DEFAULT current_timestamp(),"+
			" p VARCHAR(36) DEFAULT uuid())",
	)
	return db
}

const mariadbDefaultsQuery = "SELECT id, a, b, c, e, f, g, h, i, m FROM d"

// TestMariaDBDefaultsInSQLiteOutput: a row that takes the defaults reads the
// same in SQLite as in MariaDB, and the expression defaults are not output as
// strings (current_timestamp() becomes CURRENT_TIMESTAMP, uuid() is warned about).
func TestMariaDBDefaultsInSQLiteOutput(t *testing.T) {
	e := newEnv(t)
	e.requireMariaDB(t)
	db := mariadbDefaultsFixture(t, e)
	dir := t.TempDir()
	log := e.dumpSQLite(t, dir, datafile.Options{}, db)

	schema := readFiles(t, dir)[naming.SchemaFile(db)]
	if !strings.Contains(schema, "DEFAULT CURRENT_TIMESTAMP") {
		t.Errorf("the schema lacks DEFAULT CURRENT_TIMESTAMP:\n%s", schema)
	}
	// DEFAULT NULL of `a` is no default at all; the string NULL of `f` is one.
	for _, want := range []string{"`a` TEXT,\n", "`f` TEXT DEFAULT 'NULL',\n", "`b` TEXT DEFAULT 'x',\n", "`g` TEXT DEFAULT 'it''s',\n"} {
		if !strings.Contains(schema, want) {
			t.Errorf("the schema lacks %q:\n%s", want, schema)
		}
	}
	for _, bad := range []string{"'current_timestamp()'", "'uuid()'", "'''x'''"} {
		if strings.Contains(schema, bad) {
			t.Errorf("the schema has %s:\n%s", bad, schema)
		}
	}
	if !strings.Contains(log, "default expression of column `p` is not output") {
		t.Errorf("log lacks the warning about the uuid() default:\n%s", log)
	}

	lite := restoreSQLite(t, dir)
	if _, err := lite.Exec("INSERT INTO d (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	e.exec(t, "INSERT INTO d (id) VALUES (1)")
	want := cells(t, adminQuery{e}, mariadbDefaultsQuery)
	if got := cells(t, lite, mariadbDefaultsQuery); !reflect.DeepEqual(got, want) {
		t.Errorf("defaults differ\nMariaDB: %q\nSQLite:  %q", want, got)
	}
}

// TestMariaDBDefaultsInPostgresOutput is the same for PostgreSQL.
func TestMariaDBDefaultsInPostgresOutput(t *testing.T) {
	e := newEnv(t)
	e.requireMariaDB(t)
	pg := newPGEnv(t)
	db := mariadbDefaultsFixture(t, e)
	dir := t.TempDir()
	e.dumpPostgres(t, dir, datafile.Options{}, db)
	pg.restore(t, dir)

	conn := pg.connect(t)
	if _, err := conn.Exec(context.Background(),
		"INSERT INTO "+pgx.Identifier{db, "d"}.Sanitize()+" (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	e.exec(t, "INSERT INTO d (id) VALUES (1)")
	want := cells(t, adminQuery{e}, mariadbDefaultsQuery)
	got := pgCells(t, conn, db, "SELECT id::text, a, b, c, e, f, g, h::text, i::text, m::text FROM d")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defaults differ\nMariaDB:    %q\nPostgreSQL: %q", want, got)
	}
}
