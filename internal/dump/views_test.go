package dump

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
)

// viewRows returns the rows of `db`.view as text, in primary key order.
func (e *env) viewRows(t *testing.T, db, view string) []string {
	t.Helper()
	rows, err := e.admin.QueryContext(context.Background(),
		fmt.Sprintf("SELECT id, inc FROM `%s`.`%s` ORDER BY id", db, view))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, inc int
		if err := rows.Scan(&id, &inc); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d|%d", id, inc))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A view that calls a stored function restores only when the function is
// there already: CREATE VIEW fails with ERROR 1305 otherwise (B-116). The views
// are in a file that sorts after the routines file.
func TestViewCallingStoredFunctionRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE items (id INT PRIMARY KEY, qty INT)",
		"INSERT INTO items VALUES (1, 10), (2, 20)",
		"CREATE FUNCTION add_one(x INT) RETURNS INT DETERMINISTIC NO SQL RETURN x + 1",
		"CREATE VIEW inc AS SELECT id, add_one(qty) AS inc FROM items",
		// A view on a view, to keep the dependency order working in the new file.
		"CREATE VIEW inc2 AS SELECT id, inc FROM inc WHERE id > 0",
	)
	before := e.viewRows(t, db, "inc")
	if !reflect.DeepEqual(before, []string{"1|11", "2|21"}) {
		t.Fatalf("fixture rows = %q", before)
	}

	dir := t.TempDir()
	e.dump(t, dir, db)
	files := readFiles(t, dir)
	if strings.Contains(files[naming.SchemaFile(db)], "CREATE VIEW") {
		t.Errorf("the schema file has a view:\n%s", files[naming.SchemaFile(db)])
	}
	text, ok := files[naming.ViewsFile(db)]
	if !ok {
		t.Fatalf("no %s in %v", naming.ViewsFile(db), files)
	}
	if !strings.HasPrefix(text, datafile.Header(db)) {
		t.Errorf("views file does not start with the header:\n%.300s", text)
	}
	if strings.Index(text, "CREATE VIEW `inc`") > strings.Index(text, "CREATE VIEW `inc2`") {
		t.Errorf("view inc must come before inc2:\n%s", text)
	}

	e.roundTrip(t, dir, db)
	if after := e.viewRows(t, db, "inc"); !reflect.DeepEqual(after, before) {
		t.Errorf("view rows differ after restore\nbefore: %q\nafter:  %q", before, after)
	}
	if after := e.viewRows(t, db, "inc2"); !reflect.DeepEqual(after, before) {
		t.Errorf("view on a view differs after restore\nbefore: %q\nafter:  %q", before, after)
	}
}

func TestNoViewsNoFileAndStaleFileRemoved(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY)", "CREATE VIEW v AS SELECT id FROM a")
	dir := t.TempDir()
	e.dump(t, dir, db)
	path := filepath.Join(dir, naming.ViewsFile(db))
	if !exists(t, path) {
		t.Fatalf("%s was not written", path)
	}
	e.exec(t, "DROP VIEW v")
	e.dump(t, dir, db)
	if exists(t, path) {
		t.Errorf("%s from the earlier dump was not removed", path)
	}
	fresh := t.TempDir()
	e.dump(t, fresh, db)
	if exists(t, filepath.Join(fresh, naming.ViewsFile(db))) {
		t.Error("a database without views got a views file")
	}
}
