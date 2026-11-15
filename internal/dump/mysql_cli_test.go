package dump

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/naming"
	"mysqldance/internal/sqlvalue"
)

// mysqlCLISkip is the skip message that CI (.github/workflows/integration.yml)
// greps for: a runner that cannot reach a mysql client must fail the job
// instead of silently not testing the restore. Keep the two in step.
const mysqlCLISkip = "mysql client is not available"

// runMySQLCLI feeds script to the real mysql client inside the test container
// (`docker exec -i`), the way the image's entrypoint feeds one file of
// /docker-entrypoint-initdb.d. The client stops at the first failing statement,
// but a warning-free run must also leave stderr empty, so anything on stderr
// counts as a failure along with the exit status.
func runMySQLCLI(container, user, password string, script io.Reader) error {
	// The password goes through MYSQL_PWD: -p on the command line makes the
	// client print a warning to stderr.
	cmd := exec.Command("docker", "exec", "-i", "-e", "MYSQL_PWD="+password, container,
		"mysql", "-u"+user, "--default-character-set=utf8mb4")
	cmd.Stdin = script
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil || stderr.Len() > 0 {
		return fmt.Errorf("mysql client: %v\nstderr: %s", err, stderr.String())
	}
	return nil
}

// mysqlCLIContainer returns the container whose mysql client the tests use, or
// skips the test when MYSQLDANCE_TEST_CONTAINER is unset or docker is missing.
func mysqlCLIContainer(t *testing.T) string {
	t.Helper()
	container := os.Getenv("MYSQLDANCE_TEST_CONTAINER")
	if container == "" {
		t.Skip(mysqlCLISkip)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip(mysqlCLISkip)
	}
	return container
}

// restoreMySQLCLI restores every *.sql file of dir, in file-name order and one
// client session per file, with the mysql client of the container named by
// MYSQLDANCE_TEST_CONTAINER (the server MYSQLDANCE_TEST_DSN points at).
func (e *env) restoreMySQLCLI(t *testing.T, dir string) {
	t.Helper()
	container := mysqlCLIContainer(t)
	for _, name := range sqlFiles(t, dir) {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		err = runMySQLCLI(container, e.mc.User, e.mc.Passwd, f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("restore %s: %v", name, err)
		}
	}
}

// A database with tables, a view, triggers, routines and an event restores with
// the real mysql client, where DELIMITER is the client's own command, and
// matches the source in definitions and rows.
func TestMySQLRestoreWithClient(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE items (id INT PRIMARY KEY, qty INT, note VARCHAR(40))",
		"CREATE TABLE audit (id INT AUTO_INCREMENT PRIMARY KEY, msg VARCHAR(60))",
		// Multi-byte text checks --default-character-set; the quote and the
		// backslash check the literals.
		"INSERT INTO items VALUES (1, 10, 'one'), (2, 20, '日本語 😀'), (3, 30, 'it''s \\ ok')",
		"INSERT INTO audit (msg) VALUES ('seed')",
		"CREATE VIEW big_items AS SELECT id, note FROM items WHERE qty > 10",
		// Bodies with ';' inside, and strings holding the usual delimiter.
		"CREATE TRIGGER items_au AFTER UPDATE ON items FOR EACH ROW BEGIN\n"+
			"  IF OLD.qty <> NEW.qty THEN\n"+
			"    INSERT INTO audit (msg) VALUES (CONCAT('qty ', OLD.qty, '->', NEW.qty, ' $$ done;'));\n"+
			"  END IF;\n"+
			"END",
		"CREATE FUNCTION add_one(x INT) RETURNS INT DETERMINISTIC NO SQL RETURN x + 1",
		"CREATE PROCEDURE bump(IN delta INT, OUT total INT) COMMENT 'adds $$ delta; then sums'\n"+
			"BEGIN\n"+
			"  UPDATE items SET qty = qty + delta;\n"+
			"  SELECT SUM(qty) INTO total FROM items;\n"+
			"END",
		// Far ahead and disabled, so it never runs while the test does.
		"CREATE EVENT ev_body ON SCHEDULE AT '2037-06-01 12:00:00' ON COMPLETION PRESERVE DISABLE COMMENT 'adds $$ rows; twice'\n"+
			"DO BEGIN\n"+
			"  INSERT INTO audit (msg) VALUES ('a;b');\n"+
			"  INSERT INTO audit (msg) VALUES ('$$');\n"+
			"END",
	)
	contents, triggers, routines, events := e.contents(t, db), e.triggerDefs(t, db), e.routineDefs(t, db), e.eventDefs(t, db)
	if len(triggers) != 1 || len(routines) < 3 || len(events) != 1 {
		t.Fatalf("fixture: %d triggers, %d routine lines, %d events", len(triggers), len(routines), len(events))
	}

	dir := t.TempDir()
	e.dump(t, dir, db)
	files := readFiles(t, dir)
	for _, name := range []string{naming.TriggersFile(db), naming.RoutinesFile(db), naming.EventsFile(db)} {
		if !strings.Contains(files[name], "DELIMITER ") {
			t.Fatalf("%s has no DELIMITER line, so the client is not exercised:\n%s", name, files[name])
		}
	}

	// Skip before dropping anything when there is no client.
	mysqlCLIContainer(t)
	e.exec(t, "DROP DATABASE "+sqlvalue.QuoteIdent(db))
	e.restoreMySQLCLI(t, dir)

	if got := e.contents(t, db); !reflect.DeepEqual(got, contents) {
		t.Errorf("rows differ after restore\nbefore: %q\nafter:  %q", contents, got)
	}
	if got := e.triggerDefs(t, db); !reflect.DeepEqual(got, triggers) {
		t.Errorf("triggers differ after restore\nbefore: %q\nafter:  %q", triggers, got)
	}
	if got := e.routineDefs(t, db); !reflect.DeepEqual(got, routines) {
		t.Errorf("routines differ after restore\nbefore: %q\nafter:  %q", routines, got)
	}
	if got := e.eventDefs(t, db); !reflect.DeepEqual(got, events) {
		t.Errorf("events differ after restore\nbefore: %q\nafter:  %q", events, got)
	}
}

// A statement the server rejects fails the helper instead of passing quietly:
// the client reports it on stderr and exits non-zero.
func TestMySQLClientFailureIsReported(t *testing.T) {
	e := newEnv(t)
	container := mysqlCLIContainer(t)
	err := runMySQLCLI(container, e.mc.User, e.mc.Passwd, strings.NewReader("SELECT 1;\nSELECT * FROM mysqldance_no_such_db.no_such_table;\n"))
	if err == nil || !strings.Contains(err.Error(), "mysqldance_no_such_db") {
		t.Errorf("runMySQLCLI = %v, want an error naming the missing database", err)
	}
	if err := runMySQLCLI(container, e.mc.User, e.mc.Passwd, strings.NewReader("SELECT 1;\n")); err != nil {
		t.Errorf("a clean script failed: %v", err)
	}
}
