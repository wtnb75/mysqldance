package dump

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mysqldance/internal/naming"
	"mysqldance/internal/sqlvalue"
)

// Tests of source.Config.Jobs through Run: tables read in parallel on several
// connections that show one point in time.

// dumpJobs runs Run with jobs connections and returns the files it wrote.
func (e *env) dumpJobs(t *testing.T, jobs int, dbs ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	cfg := e.cfg
	cfg.Jobs = jobs
	if err := Run(context.Background(), Options{Source: cfg, DBs: dbs, OutDir: dir}); err != nil {
		t.Fatalf("Run(jobs=%d): %v", jobs, err)
	}
	return readFiles(t, dir)
}

// manyTables creates a database with n InnoDB tables t01, t02, ... holding a
// few rows each, and returns its name.
func (e *env) manyTables(t *testing.T, n int) string {
	t.Helper()
	db := uniqueName(t)
	e.createDB(t, db)
	for i := 1; i <= n; i++ {
		e.exec(t,
			fmt.Sprintf("CREATE TABLE t%02d (id INT PRIMARY KEY, v VARCHAR(20))", i),
			fmt.Sprintf("INSERT INTO t%02d VALUES (1, 'a%d'), (2, 'b'), (3, NULL)", i, i),
		)
	}
	return db
}

func TestJobsOutputSameAsSerial(t *testing.T) {
	e := newEnv(t)
	db := e.manyTables(t, 9)
	e.exec(t, "CREATE TABLE nokey (a INT, b INT)", "INSERT INTO nokey VALUES (2, 1), (1, 2), (1, 1)")

	serial := e.dumpJobs(t, 1, db)
	for _, jobs := range []int{2, 4, 8} {
		if got := e.dumpJobs(t, jobs, db); !reflect.DeepEqual(got, serial) {
			t.Errorf("output with %d jobs differs from the serial output:\nserial %q\nparallel %q", jobs, serial, got)
		}
	}
}

// TestJobsRowsPerFileSplitSameAsSerial: tables split into several files are
// claimed and written the same way.
func TestJobsRowsPerFileSplitSameAsSerial(t *testing.T) {
	e := newEnv(t)
	db := e.manyTables(t, 4)
	var values []string
	for i := 1; i <= 300; i++ {
		values = append(values, fmt.Sprintf("(%d, 'row %d')", i, i))
	}
	e.exec(t, "CREATE TABLE big (id INT PRIMARY KEY, v VARCHAR(20))", "INSERT INTO big VALUES "+strings.Join(values, ", "))

	run := func(jobs int) map[string]string {
		dir := t.TempDir()
		cfg := e.cfg
		cfg.Jobs = jobs
		opt := Options{Source: cfg, DBs: []string{db}, OutDir: dir}
		opt.Data.RowsPerInsert, opt.Data.MaxInsertBytes, opt.Data.RowsPerFile = 10, 1<<20, 20
		if err := Run(context.Background(), opt); err != nil {
			t.Fatalf("Run(jobs=%d): %v", jobs, err)
		}
		return readFiles(t, dir)
	}
	serial := run(1)
	if len(serial) < 10 {
		t.Fatalf("only %d files; the table was not split", len(serial))
	}
	if got := run(4); !reflect.DeepEqual(got, serial) {
		t.Errorf("split output with 4 jobs differs from the serial output")
	}
}

// TestJobsDumpIsOneInstant: a writer keeps committing one row into each of
// several InnoDB tables in a single transaction. Whatever the instant the
// dump shows, it must show the same number of rows in every table.
func TestJobsDumpIsOneInstant(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	const tables = 6
	for i := 1; i <= tables; i++ {
		e.exec(t, fmt.Sprintf("CREATE TABLE t%d (id INT PRIMARY KEY AUTO_INCREMENT, v INT)", i), fmt.Sprintf("INSERT INTO t%d (v) VALUES (0)", i))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	w := newEnv(t)
	w.exec(t, "USE "+sqlvalue.QuoteIdent(db))
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			stmts := []string{"BEGIN"}
			for i := 1; i <= tables; i++ {
				stmts = append(stmts, fmt.Sprintf("INSERT INTO t%d (v) VALUES (1)", i))
			}
			stmts = append(stmts, "COMMIT")
			for _, s := range stmts {
				if _, err := w.admin.ExecContext(context.Background(), s); err != nil {
					t.Errorf("writer %s: %v", s, err)
					return
				}
			}
		}
	})
	t.Cleanup(func() { close(stop); wg.Wait() })

	for round := range 4 {
		files := e.dumpJobs(t, 4, db)
		var counts []int
		for i := 1; i <= tables; i++ {
			data := files[naming.DataFile(db, fmt.Sprintf("t%d", i))]
			n := 0
			for line := range strings.SplitSeq(data, "\n") {
				if strings.HasPrefix(line, "(") {
					n++
				}
			}
			counts = append(counts, n)
		}
		for _, n := range counts {
			if n != counts[0] {
				t.Errorf("round %d: rows per table = %v, want all equal", round, counts)
				break
			}
		}
	}
}

// reloadUser creates an account with SELECT on db and RELOAD, so that its
// sessions can be told from the administrative ones. It is dropped at cleanup.
func (e *env) reloadUser(t *testing.T, db string) string {
	t.Helper()
	user := "mdj_" + strings.TrimPrefix(db, "mysqldance_dump_")
	e.exec(t,
		"CREATE USER '"+user+"'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON "+sqlvalue.QuoteIdent(db)+".* TO '"+user+"'@'%'",
		"GRANT RELOAD ON *.* TO '"+user+"'@'%'",
	)
	t.Cleanup(func() { e.exec(t, "DROP USER '"+user+"'@'%'") })
	return user
}

// requireNothingLeft waits until user has no session left on the server and
// the global read lock is gone (a write goes through at once).
func (e *env) requireNothingLeft(t *testing.T, user, table string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := e.admin.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER = ?", user).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions of %s are still open 10s after Run returned", n, user)
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.exec(t, "SET SESSION lock_wait_timeout = 1", "INSERT INTO "+table+" VALUES (1000)")
}

func (e *env) runAsUser(ctx context.Context, user string, jobs int, db, dir string, fileWriter func(string, *os.File) io.Writer) error {
	cfg := e.cfg
	cfg.User, cfg.Password, cfg.Jobs = user, "pw", jobs
	return Run(ctx, Options{Source: cfg, DBs: []string{db}, OutDir: dir, FileWriter: fileWriter})
}

func TestJobsWriteFailureEndsEverySession(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	e.exec(t, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM") // keeps the lock until Close
	user := e.reloadUser(t, db)
	dir := t.TempDir()
	fail := failOn(filepath.Join(dir, naming.DataFile(db, "b")))

	err := e.runAsUser(context.Background(), user, 4, db, dir, fail)
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("Run = %v, want the injected write failure", err)
	}
	e.requireNothingLeft(t, user, "a")
	if left := readFiles(t, dir); len(left) != 0 {
		t.Errorf("files left after a failed run: %q", left)
	}
}

func TestJobsCancelEndsEverySession(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	e.exec(t, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	user := e.reloadUser(t, db)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAt := filepath.Join(dir, naming.DataFile(db, "b"))
	cancelOnB := func(path string, f *os.File) io.Writer {
		if path == cancelAt {
			cancel()
		}
		return f
	}

	if err := e.runAsUser(ctx, user, 4, db, dir, cancelOnB); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	e.requireNothingLeft(t, user, "a")
	if left := readFiles(t, dir); len(left) != 0 {
		t.Errorf("files left after cancel: %q", left)
	}
}

// TestJobsErrorIsTheFirstTableInOrder: serially the error is that of the first
// failing table in order; in parallel it is the error of one of the failing
// tables, never a cancel caused by another failure.
func TestJobsErrorIsTheFirstTableInOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	db := e.manyTables(t, 8)
	failT03T07 := func(path string, f *os.File) io.Writer {
		base := filepath.Base(path)
		for _, name := range []string{"t03", "t07"} {
			if base == naming.DataFile(db, name) {
				if name == "t03" {
					// The earlier table fails later in time.
					time.Sleep(300 * time.Millisecond)
				}
				return &failingWriter{w: f, n: 10}
			}
		}
		return f
	}
	for _, jobs := range []int{1, 8} {
		cfg := e.cfg
		cfg.Jobs = jobs
		err := Run(context.Background(), Options{Source: cfg, DBs: []string{db}, OutDir: t.TempDir(), FileWriter: failT03T07})
		if err == nil || !strings.Contains(err.Error(), "injected write failure") {
			t.Fatalf("jobs=%d: Run = %v, want the injected write failure", jobs, err)
		}
		// Serially, the first table in order is the one that fails. In parallel
		// the first failure cancels the tables still running, so which real
		// error is reported depends on timing; it is one of the two.
		want := naming.DataFile(db, "t03")
		if jobs > 1 && !strings.Contains(err.Error(), want) {
			want = naming.DataFile(db, "t07")
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("jobs=%d: error %q does not name %s", jobs, err, want)
		}
	}
}
