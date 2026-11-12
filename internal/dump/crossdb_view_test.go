package dump

import (
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

// A view that reads a table of another database restores only if that database
// exists already: the files restore in file name order, and nothing orders one
// database's files against another's (B-023). The dump warns for each such view.
func TestViewReadingAnotherDatabase(t *testing.T) {
	const warn = "reads database"

	// setup makes reader's view v read the table t of ref and dumps both.
	setup := func(t *testing.T, ref, reader string) (*env, string, string) {
		t.Helper()
		e := newEnv(t)
		e.createDB(t, ref)
		e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1)")
		e.createDB(t, reader)
		e.exec(t,
			"CREATE TABLE own (id INT PRIMARY KEY)",
			"CREATE VIEW v AS SELECT id FROM "+sqlvalue.QuoteIdent(ref)+".t",
			"CREATE VIEW plain AS SELECT id FROM own",
		)
		dir := t.TempDir()
		log := e.dump(t, dir, ref, reader)
		return e, dir, log
	}

	t.Run("referenced database sorts later", func(t *testing.T) {
		reader := uniqueName(t)
		ref := reader + "2" // its files sort after reader's
		e, dir, log := setup(t, ref, reader)
		want := "view `v` reads database `" + ref + "`"
		if !strings.Contains(log, want) {
			t.Errorf("log has no warning %q:\n%s", want, log)
		}
		if n := strings.Count(log, warn); n != 1 {
			t.Errorf("log has %d warnings about views reading a database, want 1 (not for the view of its own tables):\n%s", n, log)
		}
		// The restore really fails, which is what the warning is for.
		e.exec(t, "DROP DATABASE "+sqlvalue.QuoteIdent(ref), "DROP DATABASE "+sqlvalue.QuoteIdent(reader))
		if err := e.tryRestore(t, dir); err == nil {
			t.Error("restoring the files in file name order succeeded; the view should need a database that does not exist yet")
		}
	})

	t.Run("referenced database sorts first", func(t *testing.T) {
		ref := uniqueName(t)
		reader := ref + "2"
		e, dir, log := setup(t, ref, reader)
		// Still warned: the dump does not know in what order the files restore.
		if want := "view `v` reads database `" + ref + "`"; !strings.Contains(log, want) {
			t.Errorf("log has no warning %q:\n%s", want, log)
		}
		// roundTrip drops each database right after reading it, which would leave
		// the view unreadable when its table's database is read first: read both,
		// then drop both.
		before := map[string]map[string][]string{ref: e.contents(t, ref), reader: e.contents(t, reader)}
		e.exec(t, "DROP DATABASE "+sqlvalue.QuoteIdent(reader), "DROP DATABASE "+sqlvalue.QuoteIdent(ref))
		e.restore(t, dir)
		for db, want := range before {
			if got := e.contents(t, db); !reflect.DeepEqual(got, want) {
				t.Errorf("database %s differs after restore\nbefore: %q\nafter:  %q", db, want, got)
			}
		}
	})
}
