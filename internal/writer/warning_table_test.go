package writer

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
)

// A warning that is about a table names it in Table, and the message keeps its
// text (B-090).
func TestSQLiteWarningsCarryTable(t *testing.T) {
	s := typesSchema()
	d := s.Defs["t"]
	d.Indexes = []source.IndexDef{{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id", Prefix: true}}}}
	s.Defs["t"] = d
	var got []Warning
	s.Warn = func(w Warning) { got = append(got, w) }
	if err := NewSQLite(datafile.Options{}).WriteSchema(io.Discard, s); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no warnings; the schema should have some")
	}
	for _, w := range got {
		if w.Table != "t" || !strings.HasPrefix(w.Message, "table `t`: ") {
			t.Errorf("warning = %+v, want Table t and the message kept", w)
		}
	}
}

func TestPostgresWarningsCarryTable(t *testing.T) {
	var got []Warning
	s := pgSchema()
	s.Warn = func(w Warning) { got = append(got, w) }
	if err := NewPostgres(datafile.Options{}).WriteConstraints(&bytes.Buffer{}, s); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no warnings; the foreign key into another database should have one")
	}
	for _, w := range got {
		if w.Table != "child" || !strings.HasPrefix(w.Message, "table `child`: ") {
			t.Errorf("warning = %+v, want Table child and the message kept", w)
		}
	}
}

// A warning about a view is not about a table.
func TestWarningAboutViewHasNoTable(t *testing.T) {
	var got []Warning
	views := []schemafile.View{{Name: "b", Create: "CREATE VIEW `b` AS select 1", Reads: []string{"x"}}}
	err := NewMySQL(datafile.Options{}).WriteViews(io.Discard, Schema{DB: "d", Views: views,
		Warn: func(w Warning) { got = append(got, w) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Table != "" {
		t.Errorf("warnings = %+v, want one without a Table", got)
	}
}
