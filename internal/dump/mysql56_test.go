package dump

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/naming"
)

// serverVersion returns SELECT VERSION() of the test server. Each test package
// has its own version helpers on purpose: a test that called the production
// dialectFor would be wrong together with it (R-072 in docs/review-decisions.md).
func (e *env) serverVersion(t *testing.T) string {
	t.Helper()
	var v string
	if err := e.admin.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// isMySQL56 reports whether the test server is MySQL 5.6. Tests use it only to
// leave out what 5.6 lacks (JSON, generated columns, several triggers for one
// event); production code never asks.
func (e *env) isMySQL56(t *testing.T) bool {
	t.Helper()
	return strings.HasPrefix(e.serverVersion(t), "5.6.")
}

// isMySQL5 reports whether the test server is MySQL 5.6 or 5.7, which lack
// INVISIBLE columns, CHECK constraints and the utf8mb4_0900 collations.
func (e *env) isMySQL5(t *testing.T) bool {
	t.Helper()
	return e.isMySQL56(t) || e.isMySQL57(t)
}

// skipOnMySQL56 skips the test on MySQL 5.6, whose fixture needs what 5.6 lacks.
func (e *env) skipOnMySQL56(t *testing.T, what string) {
	t.Helper()
	if e.isMySQL56(t) {
		t.Skipf("MySQL 5.6 has no %s", what)
	}
}

// requireMySQL56 skips the test unless the test server is MySQL 5.6. These
// tests check what only 5.6 does; the rest of the suite runs on every server.
func (e *env) requireMySQL56(t *testing.T) {
	t.Helper()
	if !e.isMySQL56(t) {
		t.Skip("MySQL 5.6 only")
	}
}

// TestMySQL56RoundTrip dumps a 5.6 database that has what 5.6 writes
// differently (int(11), zero dates, an implicit TIMESTAMP default, fractional
// seconds, MyISAM), restores it into 5.6 and compares every row and value, then
// restores the same files into the second server (MySQL 8.4 or MariaDB, named
// by MYSQLDANCE_TEST_RESTORE_DSN) and compares again.
func TestMySQL56RoundTrip(t *testing.T) {
	e := newEnv(t)
	e.requireMySQL56(t)
	db := uniqueName(t)
	// explicit_defaults_for_timestamp cannot be set at run time on 5.6 (a start
	// option there), and the test is about its OFF default.
	var explicitDefaults int
	if err := e.admin.QueryRowContext(context.Background(), "SELECT @@global.explicit_defaults_for_timestamp").Scan(&explicitDefaults); err != nil {
		t.Fatal(err)
	}
	if explicitDefaults != 0 {
		t.Skip("explicit_defaults_for_timestamp is ON on this server; the test is about the 5.6 default, OFF")
	}
	// A latin1 database, as the official image makes when it is not told otherwise.
	e.createDBWith(t, db, " CHARACTER SET latin1")
	// A session setting of the admin connection, so the fixture does not depend
	// on the server's configuration; the dump reads with its own session.
	e.exec(t,
		"SET SESSION sql_mode = ''",
		"CREATE TABLE md ("+
			" id INT NOT NULL PRIMARY KEY,"+
			" a VARCHAR(20) DEFAULT NULL,"+
			" b VARCHAR(40) NOT NULL DEFAULT 'it''s \\\\ ok',"+
			" u VARCHAR(40) CHARACTER SET utf8mb4 NULL,"+
			" bits BIT(5) NULL,"+
			" bl BLOB NULL,"+
			" dec_ DECIMAL(10,2) NULL,"+
			" f FLOAT NULL,"+
			" yr YEAR NULL,"+
			" d3 DATETIME(3) NULL,"+
			" t6 TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),"+
			" zd DATE NULL,"+
			" zdt DATETIME NULL,"+
			" KEY ka (a(5)))",
		"INSERT INTO md (id, a, b, u, bits, bl, dec_, f, yr, d3, t6, zd, zdt) VALUES"+
			" (1, 'x', 'line1\\nline2 \"q\"', '日本語 😀', b'10101', 0x00FF10, 1.50, 0.1, 2024,"+
			" '2024-01-02 03:04:05.678', '2024-01-02 03:04:05.123456', '0000-00-00', '2020-00-10 00:00:00'),"+
			" (2, NULL, DEFAULT, NULL, NULL, '', NULL, NULL, NULL, NULL, '2038-01-19 03:14:07.000001', NULL, '0000-00-00 00:00:00')",
		// The second TIMESTAMP NOT NULL gets DEFAULT '0000-00-00 00:00:00' by
		// itself (explicit_defaults_for_timestamp = OFF, the 5.6 default).
		"CREATE TABLE z (id INT NOT NULL PRIMARY KEY, a TIMESTAMP NOT NULL, b TIMESTAMP NOT NULL)",
		"INSERT INTO z (id, a) VALUES (1, '2024-01-02 03:04:05'), (2, '2024-06-07 08:09:10')",
		"CREATE TABLE fk_parent (id INT PRIMARY KEY)",
		"CREATE TABLE fk_child (id INT PRIMARY KEY, p INT, FOREIGN KEY (p) REFERENCES fk_parent (id) ON DELETE CASCADE)",
		"INSERT INTO fk_parent VALUES (1), (2)",
		"INSERT INTO fk_child VALUES (1, 1), (2, 2)",
		"CREATE TABLE nokey (a INT, b VARCHAR(10))",
		"INSERT INTO nokey VALUES (1, 'x'), (1, 'x'), (NULL, NULL)",
		"CREATE TABLE my (id INT PRIMARY KEY, v VARCHAR(5)) ENGINE=MyISAM",
		"INSERT INTO my VALUES (1, 'm')",
		// a_top needs m_mid, which needs z_base: name order is not creation order.
		"CREATE VIEW z_base AS SELECT id, b FROM md",
		"CREATE VIEW m_mid AS SELECT id FROM z_base",
		"CREATE VIEW a_top AS SELECT m.id FROM m_mid m JOIN nokey k ON k.a = m.id",
	)

	dir := t.TempDir()
	e.dump(t, dir, db)
	schema := readFiles(t, dir)[naming.SchemaFile(db)]
	// The server's form is kept, not converted. The zero-date default is the one
	// the server gave b of z by itself, so the dump must have it, and the first
	// TIMESTAMP keeps its CURRENT_TIMESTAMP default.
	for _, want := range []string{
		"`id` int(11) NOT NULL", "DEFAULT CHARSET=latin1",
		"`b` timestamp NOT NULL DEFAULT '0000-00-00 00:00:00'",
		"`a` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP",
		"`d3` datetime(3) DEFAULT NULL", "`t6` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)",
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("the schema lacks %q:\n%s", want, schema)
		}
	}

	before := e.contents(t, db)
	if got := before["VIEW m_mid"]; len(got) != 2 {
		t.Fatalf("m_mid rows = %q, want 2 rows", got)
	}
	// The comparison below must see the zero dates, not a NULL or an empty string.
	for _, want := range []string{`"0000-00-00"`, `"2020-00-10 00:00:00"`, `"0000-00-00 00:00:00"`} {
		if !strings.Contains(strings.Join(before["BASE TABLE md"], "\n"), want) {
			t.Fatalf("md has no %s; the test checks nothing: %q", want, before["BASE TABLE md"])
		}
	}
	for _, row := range before["BASE TABLE z"] {
		if !strings.Contains(row, "0000-00-00 00:00:00") {
			t.Fatalf("a row of z has no zero date in b: %q", row)
		}
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
