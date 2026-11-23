package dump

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/naming"
	"mysqldance/internal/sqlvalue"
)

// With KeepDefiner, a view, a routine, a trigger and an event keep their
// DEFINER, the output is deterministic, and it restores on a server where that
// user exists (B-111).
func TestKeepDefinerRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	user := "df_" + uniqueName(t)[len("mysqldance_dump_"):]
	account := sqlvalue.QuoteIdent(user) + "@'%'"
	if _, err := e.admin.ExecContext(context.Background(), "CREATE USER "+account+" IDENTIFIED BY 'pw'"); err != nil {
		t.Skipf("cannot create a test user with this DSN: %v", err)
	}
	t.Cleanup(func() {
		if _, err := e.admin.ExecContext(context.Background(), "DROP USER "+account); err != nil {
			t.Errorf("drop user %s: %v", account, err)
		}
	})
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE DEFINER="+account+" SQL SECURITY DEFINER VIEW v AS SELECT id FROM a",
		"CREATE DEFINER="+account+" FUNCTION f() RETURNS INT DETERMINISTIC RETURN 1",
		"CREATE DEFINER="+account+" TRIGGER tr BEFORE INSERT ON a FOR EACH ROW SET NEW.id = NEW.id",
		"CREATE DEFINER="+account+" EVENT ev ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' DISABLE DO SET @x = 1",
	)

	dumpKeep := func(dir string) {
		t.Helper()
		var log bytes.Buffer
		err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log), KeepDefiner: true})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	dir, again := t.TempDir(), t.TempDir()
	dumpKeep(dir)
	dumpKeep(again)
	files := readFiles(t, dir)
	if !reflect.DeepEqual(files, readFiles(t, again)) {
		t.Error("two dumps of the same database differ")
	}
	for _, name := range []string{naming.ViewsFile(db), naming.RoutinesFile(db), naming.TriggersFile(db), naming.EventsFile(db)} {
		if !strings.Contains(files[name], "DEFINER=`"+user+"`@`%`") {
			t.Errorf("%s does not keep the definer:\n%s", name, files[name])
		}
	}

	e.exec(t, "DROP DATABASE "+sqlvalue.QuoteIdent(db))
	e.restore(t, dir)
	for _, q := range []string{
		"SELECT DEFINER FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ?",
		"SELECT DEFINER FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?",
		"SELECT DEFINER FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?",
		"SELECT DEFINER FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?",
	} {
		var got string
		if err := e.admin.QueryRowContext(context.Background(), q, db).Scan(&got); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if got != user+"@%" {
			t.Errorf("%s = %q, want %q", q, got, user+"@%")
		}
	}
}
