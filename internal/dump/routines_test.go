package dump

import (
	"bytes"
	"context"
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

// routineDefs returns one line per routine of db and one per parameter:
// everything the server keeps of the definition that a restore must give back,
// except the definer (which a restore sets to the restoring user).
func (e *env) routineDefs(t *testing.T, db string) []string {
	t.Helper()
	var out []string
	rows, err := e.admin.QueryContext(context.Background(),
		"SELECT ROUTINE_NAME, ROUTINE_TYPE, IFNULL(DTD_IDENTIFIER, ''), ROUTINE_BODY, ROUTINE_DEFINITION,"+
			" IS_DETERMINISTIC, SQL_DATA_ACCESS, SECURITY_TYPE, SQL_MODE, ROUTINE_COMMENT"+
			" FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, typ, dtd, body, def, det, access, security, mode, comment string
		if err := rows.Scan(&name, &typ, &dtd, &body, &def, &det, &access, &security, &mode, &comment); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("R|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s", name, typ, dtd, body, def, det, access, security, mode, comment))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	prows, err := e.admin.QueryContext(context.Background(),
		"SELECT SPECIFIC_NAME, ORDINAL_POSITION, IFNULL(PARAMETER_MODE, ''), IFNULL(PARAMETER_NAME, ''), DTD_IDENTIFIER"+
			" FROM information_schema.PARAMETERS WHERE SPECIFIC_SCHEMA = ?", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prows.Close() }()
	for prows.Next() {
		var name, mode, pname, dtd string
		var pos int
		if err := prows.Scan(&name, &pos, &mode, &pname, &dtd); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("P|%s|%d|%s|%s|%s", name, pos, mode, pname, dtd))
	}
	if err := prows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// createRoutineFixture makes a table and routines, among them bodies with ';'
// and with the usual delimiter inside a string.
func (e *env) createRoutineFixture(t *testing.T, db string) {
	t.Helper()
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE items (id INT PRIMARY KEY, qty INT)",
		"INSERT INTO items VALUES (1, 10), (2, 20)",
		"CREATE FUNCTION add_one(x INT) RETURNS INT DETERMINISTIC NO SQL RETURN x + 1",
		// A body with ';' inside, and a string holding the usual delimiter.
		"CREATE PROCEDURE bump(IN delta INT, OUT total INT) COMMENT 'adds $$ delta; then sums'\n"+
			"BEGIN\n"+
			"  UPDATE items SET qty = qty + delta;\n"+
			"  SELECT SUM(qty) INTO total FROM items;\n"+
			"END",
		"CREATE FUNCTION label(x INT) RETURNS VARCHAR(30) DETERMINISTIC SQL SECURITY INVOKER\n"+
			"BEGIN\n"+
			"  DECLARE s VARCHAR(30) DEFAULT 'n=';\n"+
			"  RETURN CONCAT(s, x);\n"+
			"END",
		// Created under ANSI_QUOTES: "x" is an identifier-quoted string only
		// under that mode.
		"SET SESSION sql_mode = 'ANSI_QUOTES'",
		`CREATE FUNCTION ansi_q(a INT) RETURNS INT DETERMINISTIC NO SQL RETURN a * 2 + LENGTH('"')`,
		"SET SESSION sql_mode = DEFAULT",
	)
}

func TestRoutinesRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createRoutineFixture(t, db)
	before := e.routineDefs(t, db)
	if len(before) < 4 {
		t.Fatalf("fixture has too few routine lines: %q", before)
	}

	dir := t.TempDir()
	e.dump(t, dir, db)
	files := readFiles(t, dir)
	text, ok := files[naming.RoutinesFile(db)]
	if !ok {
		t.Fatalf("no %s in %v", naming.RoutinesFile(db), files)
	}
	if strings.Contains(text, "DEFINER") {
		t.Errorf("routines file names a definer:\n%s", text)
	}
	if !strings.HasPrefix(text, datafile.Header(db)+"DELIMITER ") {
		t.Errorf("routines file does not start with the header and DELIMITER:\n%.300s", text)
	}
	// Procedures first, then functions by name.
	if ib, ia := strings.Index(text, "bump"), strings.Index(text, "add_one"); ib < 0 || ia < ib {
		t.Errorf("procedure bump (%d) must come before function add_one (%d)", ib, ia)
	}

	// Restoring drops the database and loads every file in name order, with the
	// mysql client, which runs the DELIMITER lines.
	e.roundTrip(t, dir, db)
	if after := e.routineDefs(t, db); !reflect.DeepEqual(after, before) {
		t.Errorf("routines differ after restore\nbefore: %q\nafter:  %q", before, after)
	}
	// The restored routines work: the ';' in the body survived.
	var got int
	ctx := context.Background()
	if err := e.admin.QueryRowContext(ctx, "SELECT `"+db+"`.add_one(41)").Scan(&got); err != nil || got != 42 {
		t.Errorf("add_one(41) = %d, %v; want 42", got, err)
	}
	// e.admin is one connection, so the OUT parameter's session variable stays.
	if _, err := e.admin.ExecContext(ctx, "CALL `"+db+"`.bump(1, @t)"); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := e.admin.QueryRowContext(ctx, "SELECT @t").Scan(&total); err != nil || total != 32 {
		t.Errorf("after bump(1) total = %d, %v; want 32", total, err)
	}
}

func TestRoutinesOutputIsDeterministic(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createRoutineFixture(t, db)
	a, b := t.TempDir(), t.TempDir()
	e.dump(t, a, db)
	e.dump(t, b, db)
	if !reflect.DeepEqual(readFiles(t, a), readFiles(t, b)) {
		t.Error("two dumps of the same database differ")
	}
}

func TestNoRoutinesNoFileAndStaleFileRemoved(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE PROCEDURE p() SET @x = 1")
	dir := t.TempDir()
	e.dump(t, dir, db)
	path := filepath.Join(dir, naming.RoutinesFile(db))
	if !exists(t, path) {
		t.Fatalf("%s was not written", path)
	}
	e.exec(t, "DROP PROCEDURE p")
	e.dump(t, dir, db)
	if exists(t, path) {
		t.Errorf("%s from the earlier dump was not removed", path)
	}
	fresh := t.TempDir()
	e.dump(t, fresh, db)
	if exists(t, filepath.Join(fresh, naming.RoutinesFile(db))) {
		t.Error("a database without routines got a routines file")
	}
}

// Routine bodies are MySQL syntax, so a writer for another DBMS leaves them
// out, and says so for each one.
func TestRoutinesWarnedForOtherTargets(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE PROCEDURE p_a() SET @x = 1",
		"CREATE FUNCTION f_a() RETURNS INT DETERMINISTIC NO SQL RETURN 1")
	dir := t.TempDir()
	var log bytes.Buffer
	err := Run(context.Background(), Options{
		Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log), Writer: writer.NewSQLite(datafile.Options{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"level=WARN", "procedure `p_a` is not output", "function `f_a` is not output",
		"database=" + db, "routine=p_a", "routine=f_a", "type=PROCEDURE", "type=FUNCTION",
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
		if strings.Contains(ent.Name(), "routines") {
			t.Errorf("unexpected file %s", ent.Name())
		}
	}
}
