package dump

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
)

// sqliteCLISkip is the skip message that CI (.github/workflows/integration.yml)
// greps for: a runner without sqlite3 must fail the job instead of silently not
// testing the restore. Keep the two in step.
const sqliteCLISkip = "sqlite3 is not installed"

// restoreSQLiteCLI restores the *.sql files of dir with the real sqlite3
// command, through the shell line the README gives (`LC_ALL=C cat *.sql |
// sqlite3 db`), and opens the resulting database file so that its rows can be
// compared.
func restoreSQLiteCLI(t *testing.T, dir string) *sql.DB {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip(sqliteCLISkip)
	}
	if out, err := exec.Command(bin, "-version").Output(); err == nil {
		t.Logf("sqlite3 %s", strings.TrimSpace(string(out)))
	}
	file := filepath.Join(t.TempDir(), "restored.db")
	cmd := exec.Command("sh", "-c", `cat *.sql | sqlite3 "$1"`, "sh", file)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// sqlite3 keeps going after a failed statement, so stderr counts as well as
	// the exit status.
	if err := cmd.Run(); err != nil || stderr.Len() > 0 {
		t.Fatalf("sqlite3 rejected the files: %v\n%s", err, stderr.String())
	}
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The SQLite files restore with the sqlite3 command, and the row counts and the
// values match the source.
func TestSQLiteRestoreWithCommand(t *testing.T) {
	e := newEnv(t)
	db := sqliteFixture(t, e)
	dir := t.TempDir()
	e.dumpSQLite(t, dir, datafile.Options{}, db)

	lite := restoreSQLiteCLI(t, dir)
	compareSQLiteRows(t, e, db, lite)
	if got := cells(t, lite, "SELECT `table`, `from`, `to`, on_delete FROM pragma_foreign_key_list('child')"); !reflect.DeepEqual(got,
		[]string{`"parent","parent_id","id","CASCADE"`}) {
		t.Errorf("foreign keys = %q", got)
	}
	if got := cells(t, lite, "SELECT name FROM pragma_index_list('child') WHERE origin = 'c' ORDER BY name"); !reflect.DeepEqual(got,
		[]string{`"child__fk_parent"`, `"child__idx_d"`}) {
		t.Errorf("indexes of child = %q", got)
	}
}

// A table split over several files and several INSERT statements restores in
// one pass through the command.
func TestSQLiteRestoreWithCommandSplitTable(t *testing.T) {
	lite := restoreSQLiteCLI(t, dumpSplitBigTable(t, newEnv(t)))
	if got := cells(t, lite, "SELECT count(*), sum(id) FROM big"); !reflect.DeepEqual(got, []string{`"300","45150"`}) {
		t.Errorf("restored %q", got)
	}
}
