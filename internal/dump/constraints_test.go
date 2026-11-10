package dump

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/writer"
)

// constraintWriter is a MySQL writer that also writes a constraints file and
// asks for the table definitions, to test what dump does with such a writer.
type constraintWriter struct {
	*writer.MySQL
	defsSeen map[string]bool
	order    []string
}

func (c *constraintWriter) NeedsDefs() bool { return true }

func (c *constraintWriter) NewRowWriter(db string, t source.TableInfo, open writer.OpenFile) (writer.RowWriter, error) {
	c.defsSeen[t.Name] = t.Def != nil && len(t.Def.Columns) > 0
	c.order = append(c.order, "data:"+t.Name)
	return c.MySQL.NewRowWriter(db, t, open)
}

func (c *constraintWriter) WriteConstraints(w io.Writer, s writer.Schema) error {
	c.order = append(c.order, "constraints")
	_, err := io.WriteString(w, "-- constraints of "+s.DB+"\n")
	return err
}

func TestConstraintsFile(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	dir := t.TempDir()
	cw := &constraintWriter{MySQL: writer.NewMySQL(datafile.Options{}), defsSeen: map[string]bool{}}
	if err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Writer: cw}); err != nil {
		t.Fatal(err)
	}

	got := readFiles(t, dir)[naming.ConstraintsFile(db)]
	if want := "-- constraints of " + db + "\n"; got != want {
		t.Errorf("constraints file = %q, want %q", got, want)
	}
	// The constraints come after the rows of every table.
	if want := []string{"data:a", "data:b", "data:c", "constraints"}; !reflect.DeepEqual(cw.order, want) {
		t.Errorf("call order = %v, want %v", cw.order, want)
	}
	// A writer that asked for the definitions gets each table's own.
	for _, name := range []string{"a", "b", "c"} {
		if !cw.defsSeen[name] {
			t.Errorf("table %s reached NewRowWriter without its definition", name)
		}
	}

	// A later dump with a writer that has no such file removes the old one.
	e.dump(t, dir, db)
	if _, err := os.Stat(filepath.Join(dir, naming.ConstraintsFile(db))); !os.IsNotExist(err) {
		t.Errorf("the stale constraints file was not removed: %v", err)
	}
}

// A writer without constraints produces no such file.
func TestNoConstraintsFileForMySQL(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	dir := t.TempDir()
	e.dump(t, dir, db)
	if _, err := os.Stat(filepath.Join(dir, naming.ConstraintsFile(db))); !os.IsNotExist(err) {
		t.Errorf("a MySQL dump has a constraints file: %v", err)
	}
}
