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

// triggerDefs returns one line per trigger of db: everything the server keeps
// of its definition that a restore must give back.
func (e *env) triggerDefs(t *testing.T, db string) []string {
	t.Helper()
	rows, err := e.admin.QueryContext(context.Background(),
		"SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE, ACTION_TIMING, EVENT_MANIPULATION, ACTION_ORDER, SQL_MODE, ACTION_STATEMENT"+
			" FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name, table, timing, event, mode, stmt string
		var order sql.NullInt64
		if err := rows.Scan(&name, &table, &timing, &event, &order, &mode, &stmt); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s", name, table, timing, event, order.Int64, mode, stmt))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// createTriggerFixture makes tables with rows, then triggers that would change
// those rows if they ran while the data is loaded.
func (e *env) createTriggerFixture(t *testing.T, db string) {
	t.Helper()
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE items (id INT PRIMARY KEY, qty INT, note VARCHAR(40))",
		"CREATE TABLE audit (id INT AUTO_INCREMENT PRIMARY KEY, msg VARCHAR(60))",
		"INSERT INTO items VALUES (1, 10, 'one'), (2, 20, 'two')",
		"INSERT INTO audit (msg) VALUES ('seed')",
		// Two triggers for one event: they run in creation order, which is the
		// reverse of their name order.
		"CREATE TRIGGER z_first BEFORE INSERT ON items FOR EACH ROW SET NEW.note = CONCAT('z:', IFNULL(NEW.note, ''))",
		"CREATE TRIGGER a_second BEFORE INSERT ON items FOR EACH ROW SET NEW.note = CONCAT('a:', IFNULL(NEW.note, ''))",
		// A body with ';' inside, and a string holding the usual delimiter.
		"CREATE TRIGGER items_au AFTER UPDATE ON items FOR EACH ROW BEGIN\n"+
			"  IF OLD.qty <> NEW.qty THEN\n"+
			"    INSERT INTO audit (msg) VALUES (CONCAT('qty ', OLD.qty, '->', NEW.qty, ' $$ done;'));\n"+
			"  END IF;\n"+
			"END",
		"CREATE TRIGGER items_ad AFTER DELETE ON items FOR EACH ROW INSERT INTO audit (msg) VALUES ('deleted')",
		// Created under ANSI_QUOTES: "note" is an identifier, which only parses
		// under that mode.
		"SET SESSION sql_mode = 'ANSI_QUOTES'",
		`CREATE TRIGGER items_ansi BEFORE UPDATE ON items FOR EACH ROW SET NEW."note" = 'u'`,
		"SET SESSION sql_mode = DEFAULT",
	)
}

func TestTriggersRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createTriggerFixture(t, db)
	before := e.triggerDefs(t, db)
	if len(before) != 5 {
		t.Fatalf("fixture has %d triggers, want 5", len(before))
	}

	dir := t.TempDir()
	e.dump(t, dir, db)
	files := readFiles(t, dir)
	text, ok := files[naming.TriggersFile(db)]
	if !ok {
		t.Fatalf("no %s in %v", naming.TriggersFile(db), files)
	}
	if strings.Contains(text, "DEFINER") {
		t.Errorf("triggers file names a definer:\n%s", text)
	}
	if !strings.HasPrefix(text, datafile.Header(db)+"DELIMITER ") {
		t.Errorf("triggers file does not start with the header and DELIMITER:\n%.300s", text)
	}
	// The creation order, not the name order, decides: z_first before a_second.
	if iz, ia := strings.Index(text, "CREATE TRIGGER `z_first`"), strings.Index(text, "CREATE TRIGGER `a_second`"); iz < 0 || ia < iz {
		t.Errorf("z_first (%d) must come before a_second (%d) in:\n%s", iz, ia, text)
	}

	// Restoring drops the database and loads every file in name order. Rows
	// come back as dumped only if no trigger ran during the load.
	e.roundTrip(t, dir, db)
	if after := e.triggerDefs(t, db); !reflect.DeepEqual(after, before) {
		t.Errorf("triggers differ after restore\nbefore: %q\nafter:  %q", before, after)
	}
}

func TestTriggersOutputIsDeterministic(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createTriggerFixture(t, db)
	a, b := t.TempDir(), t.TempDir()
	e.dump(t, a, db)
	e.dump(t, b, db)
	fa, fb := readFiles(t, a), readFiles(t, b)
	if !reflect.DeepEqual(fa, fb) {
		t.Error("two dumps of the same database differ")
	}
}

func TestNoTriggersNoFileAndStaleFileRemoved(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE TRIGGER t BEFORE INSERT ON a FOR EACH ROW SET @x = 1")
	dir := t.TempDir()
	e.dump(t, dir, db)
	path := filepath.Join(dir, naming.TriggersFile(db))
	if !exists(t, path) {
		t.Fatalf("%s was not written", path)
	}
	e.exec(t, "DROP TRIGGER t")
	e.dump(t, dir, db)
	if exists(t, path) {
		t.Errorf("%s from the earlier dump was not removed", path)
	}
	fresh := t.TempDir()
	e.dump(t, fresh, db)
	if exists(t, filepath.Join(fresh, naming.TriggersFile(db))) {
		t.Error("a database without triggers got a triggers file")
	}
}

// Trigger bodies are MySQL syntax, so a writer for another DBMS leaves them
// out, and says so for each one.
func TestTriggersWarnedForOtherTargets(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE TRIGGER t_a BEFORE INSERT ON a FOR EACH ROW SET @x = 1")
	dir := t.TempDir()
	var log bytes.Buffer
	err := Run(context.Background(), Options{
		Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log), Writer: writer.NewSQLite(datafile.Options{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// One WARN record that carries the names as attributes.
	for _, want := range []string{
		"level=WARN", "trigger `t_a` on table `a` is not output", "database=" + db, "table=a", "trigger=t_a",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if strings.Contains(ent.Name(), "triggers") {
			t.Errorf("unexpected file %s", ent.Name())
		}
	}
}
