package dump

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/writer"
)

// eventDefs returns one line per event of db: everything the server keeps of
// the definition that a restore must give back, except the definer (which a
// restore sets to the restoring user) and the times of the last run.
func (e *env) eventDefs(t *testing.T, db string) []string {
	t.Helper()
	rows, err := e.admin.QueryContext(context.Background(),
		"SELECT EVENT_NAME, EVENT_BODY, EVENT_DEFINITION, EVENT_TYPE, EXECUTE_AT, INTERVAL_VALUE, INTERVAL_FIELD,"+
			" SQL_MODE, STARTS, ENDS, STATUS, ON_COMPLETION, EVENT_COMMENT, TIME_ZONE"+
			" FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		vals := make([]sql.NullString, 14)
		dest := make([]any, len(vals))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = v.String
			if !v.Valid {
				parts[i] = "<NULL>"
			}
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// createEventFixture makes a table and events, among them a body with ';' and
// with the usual delimiter inside a string, one created under another time
// zone and sql_mode, an enabled one and a disabled one. Every schedule is far
// ahead, so none of them runs while the tests do.
func (e *env) createEventFixture(t *testing.T, db string) {
	t.Helper()
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE log (id INT PRIMARY KEY AUTO_INCREMENT, msg VARCHAR(50))",
		"CREATE EVENT ev_enabled ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' ENABLE DO DELETE FROM log",
		// A body with ';' inside, and a string holding the usual delimiter.
		"CREATE EVENT ev_body ON SCHEDULE AT '2037-06-01 12:00:00' ON COMPLETION PRESERVE DISABLE COMMENT 'adds $$ rows; twice'\n"+
			"DO BEGIN\n"+
			"  INSERT INTO log (msg) VALUES ('a;b');\n"+
			"  INSERT INTO log (msg) VALUES ('$$');\n"+
			"END",
		// The schedule is read in the zone the event was created under, and the
		// mode changes how the body parses ("x" is a string only outside ANSI_QUOTES).
		"SET SESSION time_zone = '+09:00'",
		"SET SESSION sql_mode = 'ANSI_QUOTES'",
		`CREATE EVENT ev_tz ON SCHEDULE EVERY 1 HOUR STARTS '2037-01-01 09:00:00' ENDS '2038-01-01 09:00:00' DISABLE DO INSERT INTO log (msg) VALUES ('"')`,
		"SET SESSION sql_mode = DEFAULT",
		"SET SESSION time_zone = DEFAULT",
	)
}

func TestEventsRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createEventFixture(t, db)
	before := e.eventDefs(t, db)
	if len(before) != 3 {
		t.Fatalf("fixture has %d event lines, want 3: %q", len(before), before)
	}

	dir := t.TempDir()
	e.dump(t, dir, db)
	files := readFiles(t, dir)
	text, ok := files[naming.EventsFile(db)]
	if !ok {
		t.Fatalf("no %s in %v", naming.EventsFile(db), files)
	}
	if strings.Contains(text, "DEFINER") {
		t.Errorf("events file names a definer:\n%s", text)
	}
	if !strings.HasPrefix(text, datafile.Header(db)+"DELIMITER ") {
		t.Errorf("events file does not start with the header and DELIMITER:\n%.300s", text)
	}
	// By name: ev_body, ev_enabled, ev_tz.
	ib, ie, iz := strings.Index(text, "`ev_body`"), strings.Index(text, "`ev_enabled`"), strings.Index(text, "`ev_tz`")
	if ib < 0 || ie < ib || iz < ie {
		t.Errorf("events are not in name order (%d, %d, %d)", ib, ie, iz)
	}

	// Restoring drops the database and loads every file in name order, with the
	// mysql client, which runs the DELIMITER lines.
	e.roundTrip(t, dir, db)
	if after := e.eventDefs(t, db); !reflect.DeepEqual(after, before) {
		t.Errorf("events differ after restore\nbefore: %q\nafter:  %q", before, after)
	}
	// The restored body kept its ';': it runs as written.
	var n int
	if err := e.admin.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ? AND EVENT_DEFINITION LIKE '%a;b%'", db).Scan(&n); err != nil || n != 1 {
		t.Errorf("events with the body 'a;b' = %d, %v; want 1", n, err)
	}
}

func TestEventsOutputIsDeterministic(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createEventFixture(t, db)
	a, b := t.TempDir(), t.TempDir()
	e.dump(t, a, db)
	e.dump(t, b, db)
	if !reflect.DeepEqual(readFiles(t, a), readFiles(t, b)) {
		t.Error("two dumps of the same database differ")
	}
}

func TestNoEventsNoFileAndStaleFileRemoved(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE EVENT ev ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' DISABLE DO SET @x = 1")
	dir := t.TempDir()
	e.dump(t, dir, db)
	path := filepath.Join(dir, naming.EventsFile(db))
	if !exists(t, path) {
		t.Fatalf("%s was not written", path)
	}
	e.exec(t, "DROP EVENT ev")
	e.dump(t, dir, db)
	if exists(t, path) {
		t.Errorf("%s from the earlier dump was not removed", path)
	}
	fresh := t.TempDir()
	e.dump(t, fresh, db)
	if exists(t, filepath.Join(fresh, naming.EventsFile(db))) {
		t.Error("a database without events got an events file")
	}
}

// An enabled event whose schedule is due runs as soon as the restored server's
// scheduler sees it. The file keeps the status as it is (README "Events"), and
// the restore itself must not fail or change it.
func TestEnabledEventStaysEnabled(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE EVENT ev_on ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' ENABLE DO SET @x = 1",
		"CREATE EVENT ev_off ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' DISABLE DO SET @x = 1")
	dir := t.TempDir()
	e.dump(t, dir, db)
	text := readFiles(t, dir)[naming.EventsFile(db)]
	if !strings.Contains(text, " ENABLE DO ") || !strings.Contains(text, " DISABLE DO ") {
		t.Errorf("the status of the events is not kept:\n%s", text)
	}
	e.roundTrip(t, dir, db)
	got := map[string]string{}
	rows, err := e.admin.QueryContext(context.Background(),
		"SELECT EVENT_NAME, STATUS FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			t.Fatal(err)
		}
		got[n] = s
	}
	if want := fmt.Sprint(map[string]string{"ev_on": "ENABLED", "ev_off": "DISABLED"}); fmt.Sprint(got) != want {
		t.Errorf("status after restore = %v, want %s", got, want)
	}
}

// Event bodies are MySQL syntax, so a writer for another DBMS leaves them out,
// and says so for each one.
func TestEventsWarnedForOtherTargets(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE EVENT ev_a ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' DISABLE DO SET @x = 1")
	dir := t.TempDir()
	var log bytes.Buffer
	err := Run(context.Background(), Options{
		Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log), Writer: writer.NewSQLite(datafile.Options{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"level=WARN", "event `ev_a` is not output", "database=" + db, "event=ev_a"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if strings.Contains(ent.Name(), "events") {
			t.Errorf("unexpected file %s", ent.Name())
		}
	}
}
