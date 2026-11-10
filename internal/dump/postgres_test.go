package dump

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/writer"
)

// pgEnv is a PostgreSQL database of its own for one test, reached through the
// DSN in MYSQLDANCE_TEST_PG_DSN (a superuser, e.g.
// postgres://postgres:pg@127.0.0.1:5432/postgres?sslmode=disable). The tests
// skip without it; the PostgreSQL tests also need MySQL.
type pgEnv struct {
	dsn string // DSN of the database created for this test
}

func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()
	admin := os.Getenv("MYSQLDANCE_TEST_PG_DSN")
	if admin == "" {
		t.Skip("MYSQLDANCE_TEST_PG_DSN is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	name := uniqueName(t)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		_ = conn.Close(ctx)
	})
	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	return &pgEnv{dsn: cfg.ConnString()}
}

func (p *pgEnv) connect(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), p.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// restore runs every *.sql file of dir in file-name order, each in a session
// of its own, like the official image's entrypoint (one psql per file): a
// setting made by one file does not reach the next.
func (p *pgEnv) restore(t *testing.T, dir string) {
	t.Helper()
	for _, name := range sqlFiles(t, dir) {
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		conn, err := pgx.Connect(context.Background(), p.dsn)
		if err != nil {
			t.Fatal(err)
		}
		// ReadAll runs the script on the simple protocol, many statements at once.
		_, err = conn.PgConn().Exec(context.Background(), string(text)).ReadAll()
		_ = conn.Close(context.Background())
		if err != nil {
			t.Fatalf("PostgreSQL rejected %s: %v", name, err)
		}
	}
}

// pgCells runs query in schema and renders every cell like cells does; the
// query casts what it selects to text.
func pgCells(t *testing.T, conn *pgx.Conn, schema, query string) []string {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "SET search_path = "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		line := make([]string, len(vals))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				line[i] = "NULL"
			case string:
				line[i] = strconv.Quote(x)
			default:
				t.Fatalf("%s: column %d is %T; cast it to text", query, i+1, v)
			}
		}
		out = append(out, strings.Join(line, ","))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) dumpPostgres(t *testing.T, dir string, data datafile.Options, dbs ...string) string {
	t.Helper()
	var log bytes.Buffer
	err := Run(context.Background(), Options{
		Source: e.cfg, DBs: dbs, OutDir: dir, Log: &log, Writer: writer.NewPostgres(data),
	})
	if err != nil {
		t.Fatalf("Run(%v): %v", dbs, err)
	}
	return log.String()
}

func postgresFixture(t *testing.T, e *env) string {
	t.Helper()
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE parent (id BIGINT UNSIGNED NOT NULL PRIMARY KEY, code VARCHAR(10) NOT NULL, UNIQUE KEY uk_code (code))",
		"CREATE TABLE child ("+
			" id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,"+
			" parent_id BIGINT UNSIGNED NULL,"+
			" qty DECIMAL(10,2) NOT NULL DEFAULT 1.50,"+
			" flags BIT(64) NULL, small BIT(5) NULL, data BLOB NULL, f DOUBLE NULL, d DATE NULL,"+
			" dt DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), ts TIMESTAMP NULL, tm TIME(3) NULL,"+
			" j JSON NULL, note TEXT NULL, e ENUM('a','b') NULL, st SET('x','y','z') NULL, yr YEAR NULL,"+
			" KEY idx_d (d, id DESC),"+
			" CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parent (id) ON DELETE CASCADE)",
		"CREATE TABLE flags ("+
			" id INT PRIMARY KEY, b1 BOOLEAN NOT NULL DEFAULT TRUE, b2 TINYINT(1) NULL, b3 TINYINT(1) UNSIGNED NULL,"+
			" t4 TINYINT NULL, tu TINYINT UNSIGNED NULL, su SMALLINT UNSIGNED NULL, iu INT UNSIGNED NULL)",
		"CREATE TABLE nokey (a INT, b VARCHAR(10))",
		"CREATE TABLE gen (id INT PRIMARY KEY, x INT, y INT AS (x * 2) STORED)",
		"CREATE TABLE empty_t (id INT PRIMARY KEY)",
		"CREATE VIEW v AS SELECT id FROM child",

		"INSERT INTO parent VALUES (18446744073709551615, 'max'), (1, 'one')",
		"INSERT INTO child (parent_id, qty, flags, small, data, f, d, dt, ts, tm, j, note, e, st, yr) VALUES"+
			" (18446744073709551615, 1.10, X'FFFFFFFFFFFFFFFF', b'10101', X'00FF41', 1.5, '2024-02-29', '2024-02-29 01:02:03.456',"+
			"  '2024-03-01 12:00:00', '-838:59:59', '{\"a\": 1}', 'line1\nline2\r\nit''s \\\\ \"q\" 日本語 😀', 'b', 'x,z', 2155),"+
			" (1, -0.05, b'1', 0, '', -2.25, '1000-01-01', '2000-01-01 00:00:00', '1970-01-01 00:00:01', '838:59:59', 'null', '', 'a', '', 1901),"+
			" (NULL, DEFAULT, NULL, NULL, NULL, NULL, NULL, '2001-01-01 00:00:00', NULL, '00:00:01.5', NULL, NULL, NULL, NULL, NULL)",
		"INSERT INTO flags (id, b2, b3, t4, tu, su, iu) VALUES (1, 1, 1, -128, 255, 65535, 4294967295), (2, 0, 0, 127, 0, 0, 0), (3, NULL, NULL, NULL, NULL, NULL, NULL)",
		"INSERT INTO nokey VALUES (1, 'x'), (1, 'x'), (NULL, NULL), (0, '')",
		"INSERT INTO gen (id, x) VALUES (1, 2), (2, NULL)",
	)
	return db
}

// The acceptance test: the PostgreSQL files restore into a real PostgreSQL, one
// session per file, and the row counts and the values match the source.
func TestPostgresRoundTrip(t *testing.T) {
	e := newEnv(t)
	pg := newPGEnv(t)
	db := postgresFixture(t, e)
	dir := t.TempDir()
	log := e.dumpPostgres(t, dir, datafile.Options{}, db)

	// Nothing PostgreSQL cannot hold is dropped without a word.
	for _, want := range []string{"view `v` is not output", "generated column `y` is not output"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	want := []string{naming.SchemaFile(db), naming.ConstraintsFile(db)}
	for _, tb := range []string{"child", "empty_t", "flags", "gen", "nokey", "parent"} {
		want = append(want, naming.DataFile(db, tb))
	}
	if got := sqlFiles(t, dir); !reflect.DeepEqual(got, sortedCopy(want)) {
		t.Errorf("files = %v, want %v", got, sortedCopy(want))
	}

	pg.restore(t, dir)
	conn := pg.connect(t)
	my := adminQuery{e}
	qualified := func(table string) string { return "`" + db + "`.`" + table + "`" }
	for _, c := range []struct{ name, mysql, postgres string }{
		{"parent", "SELECT id, code FROM " + qualified("parent") + " ORDER BY id",
			"SELECT id::text, code::text FROM parent ORDER BY id"},
		{"child",
			"SELECT id, parent_id, qty, LPAD(BIN(flags+0), 64, '0'), LPAD(BIN(small+0), 5, '0'), HEX(data), f, d," +
				" DATE_FORMAT(dt, '%Y-%m-%d %H:%i:%s.%f'), DATE_FORMAT(ts, '%Y-%m-%d %H:%i:%s')," +
				" TIME_TO_SEC(tm) * 1000000 + MICROSECOND(tm), j, note, e, st, yr FROM " + qualified("child") + " ORDER BY id",
			"SELECT id::text, parent_id::text, qty::text, flags::text, small::text, upper(encode(data, 'hex')), f::text, d::text," +
				" to_char(dt, 'YYYY-MM-DD HH24:MI:SS.US'), to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')," +
				" (EXTRACT(EPOCH FROM tm) * 1000000)::bigint::text, j::text, note::text, e::text, st::text, yr::text FROM child ORDER BY id"},
		{"flags",
			"SELECT id, b1, b2, b3, t4, tu, su, iu FROM " + qualified("flags") + " ORDER BY id",
			"SELECT id::text, b1::int::text, b2::int::text, b3::int::text, t4::text, tu::text, su::text, iu::text FROM flags ORDER BY id"},
		{"nokey", "SELECT a, b FROM " + qualified("nokey") + " ORDER BY a, b",
			"SELECT a::text, b::text FROM nokey ORDER BY a NULLS FIRST, b NULLS FIRST"},
		// The generated column y is not output; the stored column x is.
		{"gen", "SELECT id, x FROM " + qualified("gen") + " ORDER BY id", "SELECT id::text, x::text FROM gen ORDER BY id"},
		{"empty_t", "SELECT id FROM " + qualified("empty_t"), "SELECT id::text FROM empty_t"},
	} {
		wantRows := cells(t, my, c.mysql)
		gotRows := pgCells(t, conn, db, c.postgres)
		if !reflect.DeepEqual(gotRows, wantRows) {
			t.Errorf("table %s differs\nMySQL:      %q\nPostgreSQL: %q", c.name, wantRows, gotRows)
		}
		if len(gotRows) == 0 && c.name != "empty_t" {
			t.Errorf("table %s restored no rows", c.name)
		}
	}

	// The types the columns got.
	types := pgCells(t, conn, db, "SELECT column_name::text, data_type::text FROM information_schema.columns"+
		" WHERE table_schema = current_schema() AND table_name = 'flags' ORDER BY ordinal_position")
	if want := []string{`"id","integer"`, `"b1","boolean"`, `"b2","boolean"`, `"b3","smallint"`, `"t4","smallint"`,
		`"tu","smallint"`, `"su","integer"`, `"iu","bigint"`}; !reflect.DeepEqual(types, want) {
		t.Errorf("types of flags = %q, want %q", types, want)
	}
	// BOOLEAN DEFAULT TRUE came along.
	if got := pgCells(t, conn, db, "SELECT column_default::text FROM information_schema.columns"+
		" WHERE table_schema = current_schema() AND table_name = 'flags' AND column_name = 'b1'"); !reflect.DeepEqual(got, []string{`"true"`}) {
		t.Errorf("default of b1 = %q", got)
	}

	// Constraints and indexes came along.
	if got := pgCells(t, conn, db, "SELECT conname::text, confdeltype::text, contype::text FROM pg_constraint"+
		" WHERE conrelid = 'child'::regclass AND contype IN ('f','p') ORDER BY conname"); !reflect.DeepEqual(got,
		[]string{`"child__PRIMARY"," ","p"`, `"fk_parent","c","f"`}) {
		t.Errorf("constraints of child = %q", got)
	}
	if got := pgCells(t, conn, db, "SELECT indexname::text FROM pg_indexes WHERE schemaname = current_schema()"+
		" AND tablename IN ('parent', 'child') ORDER BY indexname"); !reflect.DeepEqual(got,
		[]string{`"child__PRIMARY"`, `"child__fk_parent"`, `"child__idx_d"`, `"parent__PRIMARY"`, `"parent__uk_code"`}) {
		t.Errorf("indexes = %q", got)
	}
	if _, err := conn.Exec(context.Background(), "INSERT INTO parent VALUES (2, 'one')"); err == nil {
		t.Error("PostgreSQL accepted a duplicate of a UNIQUE key")
	}
	if _, err := conn.Exec(context.Background(), "INSERT INTO child (id, parent_id, dt) VALUES (99, 12345, now())"); err == nil {
		t.Error("PostgreSQL accepted a row that breaks the foreign key")
	}
	if got := pgCells(t, conn, db, "SELECT table_name::text FROM information_schema.views WHERE table_schema = current_schema()"); len(got) != 0 {
		t.Errorf("views = %q, want none", got)
	}
}

func sortedCopy(s []string) []string { return slices.Sorted(slices.Values(s)) }

// The same data gives the same bytes, and one changed row changes one line.
func TestPostgresDeterministicAndMinimalDiff(t *testing.T) {
	e := newEnv(t)
	if os.Getenv("MYSQLDANCE_TEST_PG_DSN") == "" {
		t.Skip("MYSQLDANCE_TEST_PG_DSN is not set")
	}
	db := postgresFixture(t, e)
	opt := datafile.Options{RowsPerInsert: 2}
	first, second := t.TempDir(), t.TempDir()
	e.dumpPostgres(t, first, opt, db)
	e.dumpPostgres(t, second, opt, db)
	a, b := readFiles(t, first), readFiles(t, second)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two dumps of the same data differ:\n%q\n%q", a, b)
	}

	e.exec(t, "UPDATE child SET note = 'changed' WHERE id = 2")
	third := t.TempDir()
	e.dumpPostgres(t, third, opt, db)
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

func TestPostgresSplitsLargeTable(t *testing.T) {
	e := newEnv(t)
	pg := newPGEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE big (id INT PRIMARY KEY, s VARCHAR(20))")
	var vals []string
	for i := 1; i <= 300; i++ {
		vals = append(vals, fmt.Sprintf("(%d, 'row %d')", i, i))
	}
	e.exec(t, "INSERT INTO big VALUES "+strings.Join(vals, ","))
	dir := t.TempDir()
	e.dumpPostgres(t, dir, datafile.Options{RowsPerInsert: 20, RowsPerFile: 50}, db)
	if n := len(sqlFiles(t, dir)); n < 4 {
		t.Fatalf("got %d files, want the table split into several", n)
	}
	pg.restore(t, dir)
	if got := pgCells(t, pg.connect(t), db, "SELECT count(*)::text, sum(id)::text FROM big"); !reflect.DeepEqual(got, []string{`"300","45150"`}) {
		t.Errorf("restored %q", got)
	}
}

// A dump into a directory that has the constraints file of an earlier
// PostgreSQL dump removes it when the target is MySQL again.
func TestPostgresThenMySQLDumpRemovesTheConstraintsFile(t *testing.T) {
	e := newEnv(t)
	db := postgresFixture(t, e)
	dir := t.TempDir()
	e.dumpPostgres(t, dir, datafile.Options{}, db)
	if _, err := os.Stat(filepath.Join(dir, naming.ConstraintsFile(db))); err != nil {
		t.Fatal(err)
	}
	e.dump(t, dir, db)
	if _, err := os.Stat(filepath.Join(dir, naming.ConstraintsFile(db))); !os.IsNotExist(err) {
		t.Errorf("the constraints file of the PostgreSQL dump is still there: %v", err)
	}
}

// What PostgreSQL cannot hold stops the dump with the table and column named,
// and leaves no file behind.
func TestPostgresRefusesValuesItCannotHold(t *testing.T) {
	cases := []struct {
		name, create string
		insert       []string
		want         string
	}{
		{"boolean 2", "CREATE TABLE t (id INT PRIMARY KEY, b TINYINT(1))",
			[]string{"INSERT INTO t VALUES (1, 2)"}, "`b`"},
		{"NUL in text", "CREATE TABLE t (id INT PRIMARY KEY, s TEXT)",
			[]string{"INSERT INTO t VALUES (1, CONCAT('a', CHAR(0), 'b'))"}, "`s`"},
		{"zero date", "CREATE TABLE t (id INT PRIMARY KEY, d DATE)",
			[]string{"SET SESSION sql_mode = ''", "INSERT INTO t VALUES (1, '0000-00-00')"}, "`d`"},
		{"partial date", "CREATE TABLE t (id INT PRIMARY KEY, d DATETIME)",
			[]string{"SET SESSION sql_mode = ''", "INSERT INTO t VALUES (1, '2024-00-10 01:02:03')"}, "`d`"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			db := uniqueName(t)
			e.createDB(t, db)
			e.exec(t, c.create)
			e.exec(t, c.insert...)
			dir := t.TempDir()
			err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Writer: writer.NewPostgres(datafile.Options{})})
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "`t`") {
				t.Fatalf("Run = %v, want an error naming table `t` and column %s", err, c.want)
			}
			if got := sqlFiles(t, dir); len(got) != 0 {
				t.Errorf("files left behind: %v", got)
			}
		})
	}
}

// The boolean rule depends on COLUMN_TYPE keeping tinyint(1); a TINYINT with
// another display width is a smallint.
func TestPostgresBooleanRuleOnThisServer(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY, a BOOLEAN, b BOOL, c TINYINT(1), d TINYINT(1) UNSIGNED, e TINYINT(2), f TINYINT(4), g TINYINT)")
	dir := t.TempDir()
	e.dumpPostgres(t, dir, datafile.Options{}, db)
	schema := readFiles(t, dir)[naming.SchemaFile(db)]
	for _, want := range []string{`"a" boolean`, `"b" boolean`, `"c" boolean`, `"d" smallint`,
		`"e" smallint`, `"f" smallint`, `"g" smallint`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema lacks %s:\n%s", want, schema)
		}
	}
}
