package dump

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/writer"
)

// A MySQL database whose name starts with pg_ cannot be a PostgreSQL schema of
// the same name. With --pg-schema it becomes the schema given: the files keep
// the database's name, and the schema, the rows and a foreign key in each
// direction between it and a plain database restore under the new name.
func TestPostgresSchemaNameForReservedDatabase(t *testing.T) {
	e := newEnv(t)
	pg := newPGEnv(t)
	reserved := "pg_" + uniqueName(t)
	plain := uniqueName(t)
	e.createDB(t, plain)
	e.exec(t,
		"CREATE TABLE parent (id INT NOT NULL, PRIMARY KEY (id))",
		"CREATE TABLE child (id INT NOT NULL, rid INT NOT NULL, PRIMARY KEY (id))",
		"INSERT INTO parent VALUES (1)",
	)
	e.createDB(t, reserved)
	e.exec(t,
		"CREATE TABLE parent (id INT NOT NULL, PRIMARY KEY (id))",
		"CREATE TABLE child (id INT NOT NULL, pid INT NOT NULL, PRIMARY KEY (id),"+
			" CONSTRAINT fk_in FOREIGN KEY (pid) REFERENCES parent (id),"+
			" CONSTRAINT fk_out FOREIGN KEY (pid) REFERENCES `"+plain+"`.parent (id))",
		"INSERT INTO parent VALUES (1)",
		"INSERT INTO child VALUES (1, 1)",
		"ALTER TABLE `"+plain+"`.child ADD CONSTRAINT fk_back FOREIGN KEY (rid) REFERENCES `"+reserved+"`.parent (id)",
		"INSERT INTO `"+plain+"`.child VALUES (1, 1)",
	)

	// The databases refer to each other, so neither can be dropped as it is; this
	// runs before the drops createDB registered.
	t.Cleanup(func() {
		for _, stmt := range []string{
			"ALTER TABLE `" + reserved + "`.child DROP FOREIGN KEY fk_out",
			"ALTER TABLE `" + plain + "`.child DROP FOREIGN KEY fk_back",
		} {
			if _, err := e.admin.ExecContext(context.Background(), stmt); err != nil {
				t.Errorf("%s: %v", stmt, err)
			}
		}
	})

	// Without the option the run fails and says what to do.
	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{reserved}, OutDir: t.TempDir(), Writer: writer.NewPostgres(datafile.Options{})})
	if err == nil || !strings.Contains(err.Error(), "--pg-schema") {
		t.Fatalf("Run without --pg-schema: err = %v, want one that names --pg-schema", err)
	}

	schemas, err := writer.ParsePGSchemas([]string{reserved + "=app"}, []string{reserved, plain})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err = Run(context.Background(), Options{
		Source: e.cfg, DBs: []string{reserved, plain}, OutDir: dir,
		Writer: writer.NewPostgres(datafile.Options{}).WithSchemaNames(schemas),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{naming.SchemaFile(reserved), naming.DataFile(reserved, "child"), naming.ConstraintsFile(reserved)} {
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("file name must keep the database name: %v", err)
		}
		if strings.Contains(string(text), reserved) {
			t.Errorf("%s names the MySQL database:\n%s", name, text)
		}
	}

	pg.restore(t, dir)
	conn := pg.connect(t)
	if got := pgCells(t, conn, "app", "SELECT count(*)::text FROM child"); len(got) != 1 || got[0] != `"1"` {
		t.Errorf("rows of app.child = %q", got)
	}
	// The search path is app, so the regclass of a table of app has no schema.
	got := pgCells(t, conn, "app", "SELECT conname::text, confrelid::regclass::text FROM pg_constraint WHERE contype = 'f'"+
		" AND connamespace IN ('app'::regnamespace, '\""+plain+"\"'::regnamespace) ORDER BY conname")
	want := []string{`"fk_back","parent"`, `"fk_in","parent"`, `"fk_out","` + plain + `.parent"`}
	if !slices.Equal(got, want) {
		t.Errorf("foreign keys = %q, want %q", got, want)
	}
	if got := pgCells(t, conn, "app", "SELECT count(*)::text FROM pg_namespace WHERE nspname = '"+reserved+"'"); len(got) != 1 || got[0] != `"0"` {
		t.Errorf("a schema named after the MySQL database exists: %q", got)
	}
}
