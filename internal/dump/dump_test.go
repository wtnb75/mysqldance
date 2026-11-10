package dump

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// Integration tests. They are skipped unless MYSQLDANCE_TEST_DSN is set,
// e.g. root:pw@tcp(127.0.0.1:3306)/

// testConfig converts MYSQLDANCE_TEST_DSN into a source.Config and also
// returns the parsed DSN. It skips the test when the variable is empty.
func testConfig(t *testing.T) (source.Config, *mysql.Config) {
	t.Helper()
	dsn := os.Getenv("MYSQLDANCE_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQLDANCE_TEST_DSN is not set")
	}
	mc, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse MYSQLDANCE_TEST_DSN: %v", err)
	}
	cfg := source.Config{User: mc.User, Password: mc.Passwd}
	if mc.Net == "unix" {
		cfg.Socket = mc.Addr
		return cfg, mc
	}
	host, port, err := net.SplitHostPort(mc.Addr)
	if err != nil {
		t.Fatalf("parse address %q: %v", mc.Addr, err)
	}
	cfg.Host = host
	if cfg.Port, err = strconv.Atoi(port); err != nil {
		t.Fatalf("parse port %q: %v", port, err)
	}
	return cfg, mc
}

// env is the connection state shared by one test.
type env struct {
	cfg   source.Config
	mc    *mysql.Config
	admin *sql.Conn
}

// newEnv opens a pinned administrative connection closed at cleanup.
func newEnv(t *testing.T) *env {
	t.Helper()
	cfg, mc := testConfig(t)
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &env{cfg: cfg, mc: mc, admin: conn}
}

func uniqueName(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return "mysqldance_dump_" + hex.EncodeToString(buf)
}

// createDB creates database name, drops it at cleanup and makes it the
// admin connection's default database.
func (e *env) createDB(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := e.admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+sqlvalue.QuoteIdent(name)); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	e.exec(t, "CREATE DATABASE "+sqlvalue.QuoteIdent(name), "USE "+sqlvalue.QuoteIdent(name))
}

func (e *env) exec(t *testing.T, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := e.admin.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// dump runs Run with default data options and returns what it logged.
func (e *env) dump(t *testing.T, dir string, dbs ...string) string {
	t.Helper()
	var log bytes.Buffer
	if err := Run(context.Background(), Options{Source: e.cfg, DBs: dbs, OutDir: dir, Log: &log}); err != nil {
		t.Fatalf("Run(%v): %v", dbs, err)
	}
	return log.String()
}

// restore executes every *.sql file of dir in sorted file-name order, each
// on a fresh multi-statement connection, like docker-entrypoint-initdb.d.
func (e *env) restore(t *testing.T, dir string) {
	t.Helper()
	mc := e.mc.Clone()
	mc.DBName = ""
	mc.MultiStatements = true
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// No idle connections: each file gets a new session, so session
	// settings never leak from one file to the next.
	db.SetMaxIdleConns(0)
	for _, name := range sqlFiles(t, dir) {
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(context.Background(), string(text)); err != nil {
			t.Fatalf("restore %s: %v", name, err)
		}
	}
}

// sqlFiles returns the names of the *.sql files in dir, sorted.
func sqlFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ent := range entries {
		if strings.HasSuffix(ent.Name(), ".sql") {
			out = append(out, ent.Name())
		}
	}
	sort.Strings(out)
	return out
}

// readFiles returns file name -> content for every file in dir.
func readFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, ent := range entries {
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[ent.Name()] = string(b)
	}
	return out
}

// contents returns "<TABLE_TYPE> <name>" -> rows for every table and view of
// db. Each row lists all columns (including INVISIBLE ones) in ordinal order
// as quoted bytes or NULL; rows are sorted in Go so equal-collating values
// cannot reorder them.
func (e *env) contents(t *testing.T, db string) map[string][]string {
	t.Helper()
	ctx := context.Background()
	rows, err := e.admin.QueryContext(ctx,
		"SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?", db)
	if err != nil {
		t.Fatal(err)
	}
	type obj struct{ name, typ string }
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.name, &o.typ); err != nil {
			t.Fatal(err)
		}
		objs = append(objs, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()

	out := map[string][]string{}
	for _, o := range objs {
		cols := e.columnNames(t, db, o.name)
		q := make([]string, len(cols))
		for i, c := range cols {
			q[i] = sqlvalue.QuoteIdent(c)
		}
		data, err := e.admin.QueryContext(ctx, "SELECT "+strings.Join(q, ", ")+" FROM "+sqlvalue.QuoteIdent(db)+"."+sqlvalue.QuoteIdent(o.name))
		if err != nil {
			t.Fatalf("select %s: %v", o.name, err)
		}
		raw := make([]sql.RawBytes, len(cols))
		dest := make([]any, len(raw))
		for i := range raw {
			dest[i] = &raw[i]
		}
		lines := []string{}
		for data.Next() {
			if err := data.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			vals := make([]string, len(raw))
			for i, b := range raw {
				if b == nil {
					vals[i] = "NULL"
				} else {
					vals[i] = strconv.Quote(string(b))
				}
			}
			lines = append(lines, strings.Join(vals, ","))
		}
		if err := data.Err(); err != nil {
			t.Fatal(err)
		}
		_ = data.Close()
		sort.Strings(lines)
		out[o.typ+" "+o.name] = lines
	}
	return out
}

func (e *env) columnNames(t *testing.T, db, table string) []string {
	t.Helper()
	rows, err := e.admin.QueryContext(context.Background(),
		"SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION",
		db, table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// roundTrip captures the contents of dbs, drops them, restores dir and
// checks that the restored contents are identical.
func (e *env) roundTrip(t *testing.T, dir string, dbs ...string) {
	t.Helper()
	before := map[string]map[string][]string{}
	for _, db := range dbs {
		before[db] = e.contents(t, db)
		e.exec(t, "DROP DATABASE "+sqlvalue.QuoteIdent(db))
	}
	e.restore(t, dir)
	for _, db := range dbs {
		after := e.contents(t, db)
		if !reflect.DeepEqual(after, before[db]) {
			t.Errorf("database %s differs after restore\nbefore: %q\nafter:  %q", db, before[db], after)
		}
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

func TestRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		// Foreign key cycle.
		"CREATE TABLE fk_a (id INT PRIMARY KEY, b_id INT NULL)",
		"CREATE TABLE fk_b (id INT PRIMARY KEY, a_id INT NULL, FOREIGN KEY (a_id) REFERENCES fk_a (id))",
		"ALTER TABLE fk_a ADD FOREIGN KEY (b_id) REFERENCES fk_b (id)",
		"INSERT INTO fk_a VALUES (1, NULL)",
		"INSERT INTO fk_b VALUES (1, 1), (2, 1)",
		"UPDATE fk_a SET b_id = 2 WHERE id = 1",
		// No key, with exact duplicates.
		"CREATE TABLE nokey (a INT, b VARCHAR(10))",
		"INSERT INTO nokey VALUES (1, 'x'), (1, 'x'), (NULL, NULL), (0, '')",
		// Special characters, latin1 high bytes, invalid UTF-8 in VARBINARY.
		"CREATE TABLE strs (id INT AUTO_INCREMENT PRIMARY KEY, s TEXT,"+
			" l VARCHAR(10) CHARACTER SET latin1, vb VARBINARY(10))",
		"SET SESSION sql_mode = CONCAT(@@sql_mode, ',NO_AUTO_VALUE_ON_ZERO')",
		"INSERT INTO strs VALUES (0, 'zero id', NULL, NULL)",
		"INSERT INTO strs (s, l, vb) VALUES ('', _latin1 X'FFFEE9', X'FFFE00C3'), (NULL, '', X'')",
		// Large and empty BLOBs; the large one exceeds MaxInsertBytes.
		"CREATE TABLE blobs (id INT PRIMARY KEY, b LONGBLOB)",
		"INSERT INTO blobs VALUES (1, REPEAT(UNHEX('00FF'), 800000)), (2, ''), (3, NULL)",
		"CREATE TABLE empty_t (id INT PRIMARY KEY)",
		// Only a nullable unique key, holding several NULLs.
		"CREATE TABLE nuniq (u INT NULL, v VARCHAR(5), UNIQUE KEY uk (u))",
		"INSERT INTO nuniq VALUES (NULL, 'b'), (2, 'x'), (NULL, 'a'), (NULL, 'a'), (1, NULL)",
		// Generated and INVISIBLE columns need an explicit column list.
		"CREATE TABLE gen (id INT PRIMARY KEY, x INT, y INT AS (x * 2) STORED, z INT INVISIBLE)",
		"INSERT INTO gen (id, x, z) VALUES (1, 2, 3), (2, NULL, NULL)",
		"CREATE TABLE my (id INT PRIMARY KEY, v VARCHAR(5)) ENGINE=MyISAM",
		"INSERT INTO my VALUES (1, 'm')",
		// a_top depends on z_base, so name order is not creation order.
		"CREATE VIEW z_base AS SELECT id, s FROM strs",
		"CREATE VIEW a_top AS SELECT id FROM z_base WHERE id > 0",
	)
	if _, err := e.admin.ExecContext(context.Background(), "INSERT INTO strs (s) VALUES (?)",
		"back\\slash 'single' \"double\"\nnew\r\x00nul\x1a end 日本語 😀"); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	log := e.dump(t, dir, db)
	// The MyISAM table is read under a global read lock, so no warning.
	if strings.Contains(log, "MyISAM") {
		t.Errorf("log warns about the locked MyISAM table:\n%s", log)
	}
	files := readFiles(t, dir)
	if got, want := files[naming.DataFile(db, "empty_t")], datafile.Header(db); got != want {
		t.Errorf("data file of the zero-row table = %q, want the header only %q", got, want)
	}
	schema := files[naming.SchemaFile(db)]
	for _, s := range []string{"DEFINER=", "AUTO_INCREMENT="} {
		if strings.Contains(schema, s) {
			t.Errorf("schema file contains %q:\n%s", s, schema)
		}
	}
	e.roundTrip(t, dir, db)

	// The views must have been recreated (in dependency order) and work.
	after := e.contents(t, db)
	if got := after["VIEW a_top"]; len(got) != 3 {
		t.Errorf("a_top rows = %q, want 3 rows", got)
	}
}

// lineDiff returns the number of deleted and added lines between a and b
// (LCS-based, like diff).
func lineDiff(a, b string) (del, add int) {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	common := lcs[0][0]
	return len(x) - common, len(y) - common
}

// TestNoLockWarnsAboutNonInnoDB checks that with NoLock a non-InnoDB table is
// dumped without the global read lock and a warning is logged.
func TestNoLockWarnsAboutNonInnoDB(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE my (id INT PRIMARY KEY) ENGINE=MyISAM",
		"INSERT INTO my VALUES (1)",
	)
	e.cfg.NoLock = true
	log := e.dump(t, t.TempDir(), db)
	if !strings.Contains(log, "MyISAM") || !strings.Contains(log, "`my`") {
		t.Errorf("log does not warn about the MyISAM table:\n%s", log)
	}
}

// TestOutputModes checks that FileMode applies to every output file and
// DirMode to a created output directory only.
func TestOutputModes(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1)")

	run := func(dir string) {
		t.Helper()
		opt := Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, FileMode: 0o600, DirMode: 0o700}
		if err := Run(context.Background(), opt); err != nil {
			t.Fatal(err)
		}
	}
	created := filepath.Join(t.TempDir(), "out")
	run(created)
	if got := modeOf(t, created); got != 0o700 {
		t.Errorf("created dir mode = %o, want 700", got)
	}
	for _, name := range sqlFiles(t, created) {
		if got := modeOf(t, filepath.Join(created, name)); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, got)
		}
	}

	existing := t.TempDir()
	if err := os.Chmod(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	run(existing)
	if got := modeOf(t, existing); got != 0o755 {
		t.Errorf("existing dir mode = %o, want 755 (unchanged)", got)
	}
}

func TestDeterministicAndMinimalDiff(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	var vals []string
	for i := 1; i <= 100; i++ {
		vals = append(vals, fmt.Sprintf("('row-%03d')", i))
	}
	e.exec(t,
		"CREATE TABLE t (id INT AUTO_INCREMENT PRIMARY KEY, v VARCHAR(20))",
		"INSERT INTO t (v) VALUES "+strings.Join(vals, ", "),
		"CREATE TABLE k (a INT, b VARCHAR(5))",
		"INSERT INTO k VALUES (2, 'b'), (1, 'a'), (3, NULL)",
		"CREATE VIEW vv AS SELECT a FROM k",
		"CREATE TABLE nuniq (u INT NULL, v VARCHAR(5), UNIQUE KEY uk (u))",
		"INSERT INTO nuniq VALUES (NULL, 'b'), (2, 'x'), (NULL, 'a'), (NULL, 'c'), (1, NULL)",
	)

	d1, d2 := t.TempDir(), t.TempDir()
	e.dump(t, d1, db)
	// Advance the AUTO_INCREMENT counter without changing the data, and
	// re-insert the same rows in another order.
	e.exec(t,
		"INSERT INTO t (v) VALUES ('tmp')", "DELETE FROM t WHERE v = 'tmp'",
		"DELETE FROM nuniq", "DELETE FROM k",
		"INSERT INTO nuniq VALUES (1, NULL), (NULL, 'c'), (NULL, 'a'), (2, 'x'), (NULL, 'b')",
		"INSERT INTO k VALUES (3, NULL), (1, 'a'), (2, 'b')",
	)
	e.dump(t, d2, db)
	f1, f2 := readFiles(t, d1), readFiles(t, d2)
	if !reflect.DeepEqual(f1, f2) {
		t.Fatalf("two dumps of the same data differ")
	}

	e.exec(t, "UPDATE t SET v = 'ROW-050' WHERE id = 50")
	d3 := t.TempDir()
	e.dump(t, d3, db)
	f3 := readFiles(t, d3)
	if len(f3) != len(f1) {
		t.Fatalf("file sets differ: %d vs %d files", len(f1), len(f3))
	}
	var del, add int
	for name, old := range f1 {
		cur, ok := f3[name]
		if !ok {
			t.Fatalf("file %s missing after update", name)
		}
		d, a := lineDiff(old, cur)
		del += d
		add += a
	}
	if del != 1 || add != 1 {
		t.Errorf("diff after updating one row = -%d +%d, want -1 +1", del, add)
	}
}

// dumpSplit runs Run with RowsPerFile set.
func (e *env) dumpSplit(t *testing.T, dir string, rowsPerFile uint32, dbs ...string) {
	t.Helper()
	opt := Options{Source: e.cfg, DBs: dbs, OutDir: dir, Data: datafile.Options{RowsPerFile: rowsPerFile}}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run(%v): %v", dbs, err)
	}
}

func TestSplitLargeTable(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	var vals []string
	for i := 1; i <= 2000; i++ {
		vals = append(vals, fmt.Sprintf("(%d, 'row-%04d')", i, i))
	}
	var nokeyVals []string
	for i := 0; i < 600; i++ {
		nokeyVals = append(nokeyVals, fmt.Sprintf("(%d, 'k%d')", i%450, i%450))
	}
	e.exec(t,
		"CREATE TABLE big (id INT PRIMARY KEY, v VARCHAR(20))",
		"INSERT INTO big VALUES "+strings.Join(vals, ", "),
		"CREATE TABLE small (id INT PRIMARY KEY)",
		"INSERT INTO small VALUES (1), (2), (3)",
		"CREATE TABLE empty_t (id INT PRIMARY KEY)",
		"CREATE TABLE nokey (a INT, b VARCHAR(10))",
		// No key, with duplicates: split by the hash of the whole row.
		"INSERT INTO nokey VALUES "+strings.Join(nokeyVals, ", ")+", (1, 'x'), (1, 'x'), (NULL, NULL)",
	)

	dir := t.TempDir()
	e.dumpSplit(t, dir, 100, db)
	files := readFiles(t, dir)
	var parts []string
	for name := range files {
		if strings.HasPrefix(name, naming.EscapeName(db)+".10-data.big.") {
			parts = append(parts, name)
		}
	}
	sort.Strings(parts)
	if len(parts) < 5 {
		t.Fatalf("big was split into %d files, want several: %q", len(parts), parts)
	}
	if want := naming.DataFileParts(db, "big", len(parts)); !reflect.DeepEqual(parts, want) {
		t.Errorf("part names = %q, want %q", parts, want)
	}
	for _, tbl := range []string{"small", "empty_t"} {
		if _, ok := files[naming.DataFile(db, tbl)]; !ok {
			t.Errorf("table %s should keep its unnumbered file", tbl)
		}
	}
	if _, ok := files[naming.DataFileParts(db, "nokey", 2)[0]]; !ok {
		t.Error("the keyless table was not split")
	}
	for _, p := range parts {
		if !strings.HasPrefix(files[p], datafile.Header(db)) {
			t.Errorf("%s does not start with the fixed header", p)
		}
	}

	// Loading the files in lexicographic order restores the same data.
	e.roundTrip(t, dir, db)

	// Updating one row changes only that row, and no boundary moves.
	// The round trip dropped and recreated the database, so select it again.
	e.exec(t, "USE "+sqlvalue.QuoteIdent(db), "UPDATE big SET v = 'CHANGED' WHERE id = 1000")
	dir2 := t.TempDir()
	e.dumpSplit(t, dir2, 100, db)
	files2 := readFiles(t, dir2)
	if len(files2) != len(files) {
		t.Fatalf("file sets differ: %d vs %d files", len(files), len(files2))
	}
	var del, add int
	for name, old := range files {
		cur, ok := files2[name]
		if !ok {
			t.Fatalf("file %s missing after the update", name)
		}
		d, a := lineDiff(old, cur)
		del += d
		add += a
	}
	if del != 1 || add != 1 {
		t.Errorf("diff after updating one row = -%d +%d, want -1 +1", del, add)
	}

	// Dumping again without a split replaces the numbered files.
	e.dumpSplit(t, dir2, 0, db)
	files3 := readFiles(t, dir2)
	for _, p := range parts {
		if _, ok := files3[p]; ok {
			t.Errorf("numbered file %s was not removed", p)
		}
	}
	if _, ok := files3[naming.DataFile(db, "big")]; !ok {
		t.Error("unnumbered file of big is missing")
	}
}

func TestStaleFilesRemoved(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE t1 (id INT PRIMARY KEY)",
		"CREATE TABLE t2 (id INT PRIMARY KEY)",
		"INSERT INTO t1 VALUES (1)",
		"INSERT INTO t2 VALUES (2)",
	)
	dir := t.TempDir()
	// Files that must survive: an unrelated file and a file of a database
	// whose name has db as a prefix.
	keep := []string{"README.txt", naming.DataFile(db+"2", "x"), naming.SchemaFile(db + "2")}
	for _, name := range keep {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	e.dump(t, dir, db)
	t2 := filepath.Join(dir, naming.DataFile(db, "t2"))
	if !exists(t, t2) {
		t.Fatalf("%s was not written", t2)
	}

	e.exec(t, "DROP TABLE t2")
	e.dump(t, dir, db)
	if exists(t, t2) {
		t.Errorf("stale data file %s was not removed", t2)
	}
	if !exists(t, filepath.Join(dir, naming.DataFile(db, "t1"))) {
		t.Errorf("data file of t1 is missing")
	}
	for _, name := range keep {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != "keep\n" {
			t.Errorf("unrelated file %s was touched: %q, %v", name, b, err)
		}
	}
}

func TestOwnedNameSymlinkRefused(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE t1 (id INT PRIMARY KEY)", "INSERT INTO t1 VALUES (1)")
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, naming.DataFile(db, "t1"))
	if err := os.Symlink(sentinel, link); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir})
	if err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("Run = %v, want an error naming %s", err, link)
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "sentinel\n" {
		t.Errorf("sentinel changed: %q, %v", b, err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink %s was replaced: %v, %v", link, fi, err)
	}
}

// failingWriter fails once more than n bytes have been written.
type failingWriter struct {
	w io.Writer
	n int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if len(p) > f.n {
		k, _ := f.w.Write(p[:f.n])
		f.n = 0
		return k, errors.New("injected write failure")
	}
	f.n -= len(p)
	return f.w.Write(p)
}

// failOn makes writing the file at failing (its final path) fail after part
// of its header, calling before first if it is not nil, and restores the hook
// at cleanup.
func failOn(t *testing.T, failing string, before func()) {
	t.Helper()
	hookFileWriter(t, func(path string, f *os.File) io.Writer {
		if path == failing {
			if before != nil {
				before()
			}
			return &failingWriter{w: f, n: 10}
		}
		return f
	})
}

// hookFileWriter replaces fileWriter until the end of the test.
func hookFileWriter(t *testing.T, hook func(path string, f *os.File) io.Writer) {
	t.Helper()
	orig := fileWriter
	t.Cleanup(func() { fileWriter = orig })
	fileWriter = hook
}

// threeTables creates a database with tables a, b and c and returns its name.
func (e *env) threeTables(t *testing.T) string {
	t.Helper()
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE a (id INT PRIMARY KEY)", "INSERT INTO a VALUES (1)",
		"CREATE TABLE b (id INT PRIMARY KEY)", "INSERT INTO b VALUES (1), (2)",
		"CREATE TABLE c (id INT PRIMARY KEY)", "INSERT INTO c VALUES (3)",
	)
	return db
}

func TestWriteFailureKeepsPreviousDump(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.dump(t, dir, db)
	before := readFiles(t, dir)

	// The data changes, a table disappears and another is added, so a partly
	// applied second dump would differ from the first in every way.
	e.exec(t, "INSERT INTO a VALUES (10)", "DROP TABLE c", "CREATE TABLE d (id INT PRIMARY KEY)")
	failOn(t, filepath.Join(dir, naming.DataFile(db, "b")), nil)

	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir})
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("Run = %v, want the injected write failure", err)
	}
	if after := readFiles(t, dir); !reflect.DeepEqual(after, before) {
		t.Errorf("previous dump changed after a failed run:\nbefore %q\nafter  %q", before, after)
	}
}

func TestCancelKeepsPreviousDump(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	dir := t.TempDir()
	e.dump(t, dir, db)
	before := readFiles(t, dir)

	e.exec(t, "INSERT INTO a VALUES (10)")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel, as Ctrl-C does, while the data file of b is being created. No
	// write fails, so the run stops because reading c sees the cancel.
	cancelAt := filepath.Join(dir, naming.DataFile(db, "b"))
	hookFileWriter(t, func(path string, f *os.File) io.Writer {
		if path == cancelAt {
			cancel()
		}
		return f
	})

	if err := Run(ctx, Options{Source: e.cfg, DBs: []string{db}, OutDir: dir}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if after := readFiles(t, dir); !reflect.DeepEqual(after, before) {
		t.Errorf("previous dump changed after cancel:\nbefore %q\nafter  %q", before, after)
	}
}

func TestFailureInLaterDatabaseKeepsEarlierOnes(t *testing.T) {
	e := newEnv(t)
	db1 := e.threeTables(t)
	db2 := e.threeTables(t)
	dir := t.TempDir()
	e.dump(t, dir, db1, db2)
	before := readFiles(t, dir)

	e.exec(t, "USE "+sqlvalue.QuoteIdent(db1), "INSERT INTO a VALUES (10)")
	failOn(t, filepath.Join(dir, naming.DataFile(db2, "b")), nil)

	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db1, db2}, OutDir: dir})
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("Run = %v, want the injected write failure", err)
	}
	if after := readFiles(t, dir); !reflect.DeepEqual(after, before) {
		t.Errorf("dump of the earlier database changed:\nbefore %q\nafter  %q", before, after)
	}
}

func TestSymlinkAppearingDuringRunRefused(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	dir := t.TempDir()
	e.dump(t, dir, db)
	before := readFiles(t, dir)

	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An owned name that is not a regular file appears after the early check,
	// while the data file of b is being created.
	link := filepath.Join(dir, naming.DataFile(db, "zz"))
	hookFileWriter(t, func(path string, f *os.File) io.Writer {
		if path == filepath.Join(dir, naming.DataFile(db, "b")) {
			if err := os.Symlink(sentinel, link); err != nil {
				t.Error(err)
			}
		}
		return f
	})
	e.exec(t, "INSERT INTO a VALUES (10)")

	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir})
	if err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("Run = %v, want an error naming %s", err, link)
	}
	after := readFiles(t, dir)
	delete(after, filepath.Base(link))
	if !reflect.DeepEqual(after, before) {
		t.Errorf("previous dump changed:\nbefore %q\nafter  %q", before, after)
	}
}

func TestTempFiles(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	dir := t.TempDir()
	// A temp file left by a killed run is swept; a similar file that is not
	// ours is not.
	leftover := filepath.Join(dir, ".mysqldance-0001.tmp")
	other := filepath.Join(dir, ".other.tmp")
	for _, p := range []string{leftover, other} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.dump(t, dir, db)
	if exists(t, leftover) {
		t.Errorf("leftover temp file %s was not swept", leftover)
	}
	if !exists(t, other) {
		t.Errorf("unrelated file %s was removed", other)
	}
	for name := range readFiles(t, dir) {
		if strings.HasPrefix(name, ".mysqldance-") {
			t.Errorf("temp file %s left after a successful run", name)
		}
	}
}

func TestOddNamesAndCaseCollision(t *testing.T) {
	e := newEnv(t)

	t.Run("odd names", func(t *testing.T) {
		db := uniqueName(t)
		e.createDB(t, db)
		names := []string{"with space", "back`tick", "日本語", "dot.name", "Upper", "per%cent"}
		for i, n := range names {
			e.exec(t,
				"CREATE TABLE "+sqlvalue.QuoteIdent(n)+" (id INT PRIMARY KEY, "+sqlvalue.QuoteIdent("col "+n)+" VARCHAR(20))",
				fmt.Sprintf("INSERT INTO %s VALUES (%d, 'v%d')", sqlvalue.QuoteIdent(n), i, i),
			)
		}
		e.exec(t, "CREATE VIEW "+sqlvalue.QuoteIdent("view `"+names[2])+" AS SELECT * FROM "+sqlvalue.QuoteIdent(names[2]))
		dir := t.TempDir()
		e.dump(t, dir, db)
		for _, n := range names {
			if !exists(t, filepath.Join(dir, naming.DataFile(db, n))) {
				t.Errorf("data file for %q is missing", n)
			}
		}
		e.roundTrip(t, dir, db)
	})

	t.Run("case collision", func(t *testing.T) {
		var lctn int
		if err := e.admin.QueryRowContext(context.Background(), "SELECT @@lower_case_table_names").Scan(&lctn); err != nil {
			t.Fatal(err)
		}
		if lctn != 0 {
			t.Skipf("lower_case_table_names = %d", lctn)
		}
		db := uniqueName(t)
		e.createDB(t, db)
		e.exec(t, "CREATE TABLE `Users` (id INT)", "CREATE TABLE `users` (id INT)")
		dir := t.TempDir()
		old := filepath.Join(dir, naming.DataFile(db, "old"))
		if err := os.WriteFile(old, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir})
		if err == nil || !strings.Contains(err.Error(), "Users") || !strings.Contains(err.Error(), "users") {
			t.Fatalf("Run = %v, want a case-insensitive collision error naming Users and users", err)
		}
		if got := readFiles(t, dir); len(got) != 1 || got[filepath.Base(old)] != "old\n" {
			t.Errorf("output directory changed on error: %q", got)
		}
	})

	t.Run("file name too long", func(t *testing.T) {
		db := uniqueName(t)
		e.createDB(t, db)
		long := strings.Repeat("あ", 30)
		e.exec(t, "CREATE TABLE ok (id INT)", "CREATE TABLE "+sqlvalue.QuoteIdent(long)+" (id INT)")
		dir := t.TempDir()
		err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir})
		if err == nil || !strings.Contains(err.Error(), long) || !strings.Contains(err.Error(), "255") {
			t.Fatalf("Run = %v, want a 255-byte file name error naming the table", err)
		}
		if got := readFiles(t, dir); len(got) != 0 {
			t.Errorf("files written despite the error: %q", got)
		}
	})
}

func TestPartitionedTablesDeterministic(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE pr (id INT AUTO_INCREMENT, v VARCHAR(10), PRIMARY KEY (id))"+
			" PARTITION BY RANGE (id) (PARTITION p0 VALUES LESS THAN (10) COMMENT 'p\n) x',"+
			" PARTITION p1 VALUES LESS THAN MAXVALUE)",
		"CREATE TABLE ph (id INT AUTO_INCREMENT PRIMARY KEY, v INT) PARTITION BY HASH (id) PARTITIONS 3",
		"CREATE TABLE pk (id INT AUTO_INCREMENT PRIMARY KEY, v INT) PARTITION BY KEY (id) PARTITIONS 2",
		"INSERT INTO pr (v) VALUES ('a'), ('b'), ('c')",
		"INSERT INTO ph (v) VALUES (1), (2)",
		"INSERT INTO pk (v) VALUES (1)",
	)
	d1, d2 := t.TempDir(), t.TempDir()
	e.dump(t, d1, db)
	// Advance every AUTO_INCREMENT counter without changing the data.
	for _, tbl := range []string{"pr", "ph", "pk"} {
		e.exec(t, "INSERT INTO "+tbl+" (v) VALUES ('99')", "DELETE FROM "+tbl+" WHERE v = '99'")
	}
	e.dump(t, d2, db)
	f1, f2 := readFiles(t, d1), readFiles(t, d2)
	schema := f1[naming.SchemaFile(db)]
	if strings.Contains(schema, "AUTO_INCREMENT=") || !strings.Contains(schema, "PARTITION BY HASH") {
		t.Errorf("schema file keeps a counter or lost the partitioning:\n%s", schema)
	}
	if !reflect.DeepEqual(f1, f2) {
		t.Fatalf("dumps differ after advancing the counters:\n%s\n---\n%s", schema, f2[naming.SchemaFile(db)])
	}
	e.roundTrip(t, d2, db)
}

// TestColumnPrivilegesRefused dumps as an account that lacks SELECT on some
// columns. With another privilege on the table (here the database-level
// SHOW VIEW that dump accounts usually hold) SHOW CREATE TABLE succeeds while
// information_schema hides the columns; that must be an error rather than a
// dump that silently drops them. With column grants alone SHOW CREATE TABLE
// itself is denied.
func TestColumnPrivilegesRefused(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE cp (id INT PRIMARY KEY, a INT, g INT AS (a * 2) VIRTUAL, h INT INVISIBLE, s INT)",
		"INSERT INTO cp (id, a, h, s) VALUES (1, 2, 3, 4)",
		"CREATE TABLE ok (id INT PRIMARY KEY)",
	)
	showView := "SHOW VIEW ON " + sqlvalue.QuoteIdent(db) + ".*"
	for name, c := range map[string]struct {
		grants  []string
		missing string // column the error must name, if any
	}{
		// Every visible column but the INVISIBLE one: SELECT * would succeed.
		"invisible missing":  {[]string{"SELECT (id, a, g, s) ON " + sqlvalue.QuoteIdent(db) + ".cp", showView}, "`h`"},
		"visible missing":    {[]string{"SELECT (id, a, g) ON " + sqlvalue.QuoteIdent(db) + ".cp", showView}, "`s`"},
		"column grants only": {[]string{"SELECT (id, a, g, s) ON " + sqlvalue.QuoteIdent(db) + ".cp"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			user := "md_" + uniqueName(t)[len("mysqldance_dump_"):]
			account := sqlvalue.QuoteIdent(user) + "@'%'"
			if _, err := e.admin.ExecContext(context.Background(), "CREATE USER "+account+" IDENTIFIED BY 'pw'"); err != nil {
				t.Skipf("cannot create a test user with this DSN: %v", err)
			}
			t.Cleanup(func() {
				if _, err := e.admin.ExecContext(context.Background(), "DROP USER IF EXISTS "+account); err != nil {
					t.Errorf("drop user %s: %v", account, err)
				}
			})
			for _, g := range append(c.grants, "SELECT ON "+sqlvalue.QuoteIdent(db)+".ok") {
				e.exec(t, "GRANT "+g+" TO "+account)
			}
			cfg := e.cfg
			cfg.User, cfg.Password = user, "pw"
			dir := t.TempDir()
			err := Run(context.Background(), Options{Source: cfg, DBs: []string{db}, OutDir: dir})
			if err == nil || !strings.Contains(err.Error(), "`cp`") || !strings.Contains(err.Error(), c.missing) {
				t.Fatalf("Run = %v, want an error naming table `cp` and column %s", err, c.missing)
			}
			t.Logf("Run error: %v", err)
			if got := readFiles(t, dir); len(got) != 0 {
				t.Errorf("files written despite the error: %q", got)
			}
		})
	}
}

func TestMultipleDatabases(t *testing.T) {
	e := newEnv(t)
	db1 := uniqueName(t)
	db2 := db1 + "2" // shares db1 as a prefix
	e.createDB(t, db1)
	e.exec(t,
		"CREATE TABLE a (id INT PRIMARY KEY)", "INSERT INTO a VALUES (1)",
		"CREATE TABLE zz (id INT PRIMARY KEY)", "INSERT INTO zz VALUES (2)",
	)
	e.createDB(t, db2)
	e.exec(t,
		"CREATE TABLE a (id INT PRIMARY KEY)", "INSERT INTO a VALUES (3)",
		"CREATE TABLE b (id INT PRIMARY KEY, a_id INT, FOREIGN KEY (a_id) REFERENCES a (id))",
		"INSERT INTO b VALUES (1, 3)",
	)

	dir := t.TempDir()
	e.dump(t, dir, db1, db2)
	files := sqlFiles(t, dir)
	if len(files) != 6 {
		t.Fatalf("files = %q, want 6", files)
	}
	for _, db := range []string{db1, db2} {
		var idx []int
		for i, f := range files {
			if naming.IsOwnedBy(f, db) {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 || files[idx[0]] != naming.SchemaFile(db) {
			t.Errorf("%s: schema file does not sort first among %q", db, files)
			continue
		}
		if idx[len(idx)-1]-idx[0] != len(idx)-1 {
			t.Errorf("%s: files are not contiguous in %q", db, files)
		}
	}

	// Re-dumping one database leaves the other's files untouched.
	before := readFiles(t, dir)
	e.exec(t, "USE "+sqlvalue.QuoteIdent(db1), "CREATE TABLE c (id INT PRIMARY KEY)")
	e.dump(t, dir, db1)
	after := readFiles(t, dir)
	for name, content := range before {
		if naming.IsOwnedBy(name, db2) && after[name] != content {
			t.Errorf("%s changed when only %s was dumped", name, db1)
		}
	}
	if _, ok := after[naming.DataFile(db1, "c")]; !ok {
		t.Errorf("data file of new table c is missing")
	}

	e.roundTrip(t, dir, db1, db2)
}
