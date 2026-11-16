package dump

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/sqlvalue"
)

// crossDBFixture makes two databases, a and b (b's files sort after a's), each
// with a table c whose column r references the table t of the other one, and
// returns their names.
func crossDBFixture(t *testing.T, e *env) (a, b string) {
	t.Helper()
	a = uniqueName(t)
	b = a + "2"
	qa, qb := sqlvalue.QuoteIdent(a), sqlvalue.QuoteIdent(b)
	e.createDB(t, a)
	e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1), (2)")
	e.createDB(t, b)
	e.exec(t,
		"CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (10), (20)",
		"CREATE TABLE c (id INT PRIMARY KEY, r INT, CONSTRAINT fk_to_a FOREIGN KEY (r) REFERENCES "+qa+".t (id))",
		"INSERT INTO c VALUES (1, 2)",
		"USE "+qa,
		"CREATE TABLE c (id INT PRIMARY KEY, r INT, CONSTRAINT fk_to_b FOREIGN KEY (r) REFERENCES "+qb+".t (id))",
		"INSERT INTO c VALUES (1, 20)",
	)
	// The databases refer to each other, so neither can be dropped as it is; this
	// runs before the drops createDB registered.
	t.Cleanup(func() {
		if _, err := e.admin.ExecContext(context.Background(), "ALTER TABLE "+qa+".c DROP FOREIGN KEY fk_to_b"); err != nil {
			t.Errorf("drop the foreign key: %v", err)
		}
	})
	return a, b
}

// Foreign keys from a to b and from b to a restore into PostgreSQL, whichever
// database has the keys, and they hold afterwards.
func TestPostgresForeignKeysBetweenDatabases(t *testing.T) {
	e := newEnv(t)
	pg := newPGEnv(t)
	a, b := crossDBFixture(t, e)
	dir := t.TempDir()
	log := e.dumpPostgres(t, dir, datafile.Options{}, a, b)
	if strings.Contains(log, "references another database") || strings.Contains(log, "not dumped") {
		t.Errorf("a foreign key between dumped databases was reported:\n%s", log)
	}

	// Both keys are in b's constraints file, which runs last; a's has none.
	files := readFiles(t, dir)
	if strings.Contains(files[naming.ConstraintsFile(a)], "ADD CONSTRAINT") {
		t.Errorf("a's constraints file has a key:\n%s", files[naming.ConstraintsFile(a)])
	}
	if n := strings.Count(files[naming.ConstraintsFile(b)], "ADD CONSTRAINT"); n != 2 {
		t.Errorf("b's constraints file has %d keys, want 2:\n%s", n, files[naming.ConstraintsFile(b)])
	}

	pg.restore(t, dir)
	conn := pg.connect(t)
	got := pgCells(t, conn, a, "SELECT n.nspname::text || '.' || c.relname::text, fn.nspname::text || '.' || f.relname::text"+
		" FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace"+
		" JOIN pg_class f ON f.oid = k.confrelid JOIN pg_namespace fn ON fn.oid = f.relnamespace"+
		" WHERE k.contype = 'f' ORDER BY 1")
	if want := []string{`"` + a + `.c","` + b + `.t"`, `"` + b + `.c","` + a + `.t"`}; !reflect.DeepEqual(got, want) {
		t.Errorf("foreign keys = %q, want %q", got, want)
	}
	if got := pgCells(t, conn, a, "SELECT r::text FROM c"); !reflect.DeepEqual(got, []string{`"20"`}) {
		t.Errorf("rows of a.c = %q", got)
	}
	// The keys work in both directions.
	for _, stmt := range []string{
		"INSERT INTO " + pgQuote(a) + ".c VALUES (2, 999)",
		"INSERT INTO " + pgQuote(b) + ".c VALUES (2, 999)",
	} {
		if _, err := conn.Exec(context.Background(), stmt); err == nil {
			t.Errorf("PostgreSQL accepted %s although the key forbids it", stmt)
		}
	}
	for _, stmt := range []string{
		"INSERT INTO " + pgQuote(a) + ".c VALUES (2, 10)",
		"INSERT INTO " + pgQuote(b) + ".c VALUES (2, 1)",
	} {
		if _, err := conn.Exec(context.Background(), stmt); err != nil {
			t.Errorf("%s: %v", stmt, err)
		}
	}
}

func pgQuote(name string) string { return sqlvalue.QuotePGIdent(name) }

// The order the databases are named in does not change a byte of the output.
func TestPostgresCrossDatabaseOutputDoesNotDependOnTheOrderOfDatabases(t *testing.T) {
	e := newEnv(t)
	if os.Getenv("MYSQLDANCE_TEST_PG_DSN") == "" {
		t.Skip("MYSQLDANCE_TEST_PG_DSN is not set")
	}
	a, b := crossDBFixture(t, e)
	ab, ba := t.TempDir(), t.TempDir()
	e.dumpPostgres(t, ab, datafile.Options{}, a, b)
	e.dumpPostgres(t, ba, datafile.Options{}, b, a)
	if x, y := readFiles(t, ab), readFiles(t, ba); !reflect.DeepEqual(x, y) {
		t.Errorf("the output depends on the order of the databases\n%q\n%q", x, y)
	}
}

// With the other database left out of the dump, its key is reported and not
// output, and what is left restores.
func TestPostgresForeignKeyIntoADatabaseThatWasNotDumped(t *testing.T) {
	e := newEnv(t)
	pg := newPGEnv(t)
	a, b := crossDBFixture(t, e)
	dir := t.TempDir()
	log := e.dumpPostgres(t, dir, datafile.Options{}, a)
	if want := "foreign key `fk_to_b` references database `" + b + "`, which is not dumped"; !strings.Contains(log, want) {
		t.Errorf("log lacks %q:\n%s", want, log)
	}
	if got := readFiles(t, dir)[naming.ConstraintsFile(a)]; strings.Contains(got, "ADD CONSTRAINT") {
		t.Errorf("the key was written:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, naming.ConstraintsFile(b))); !os.IsNotExist(err) {
		t.Errorf("a constraints file of the database that was not dumped: %v", err)
	}
	pg.restore(t, dir)
}
