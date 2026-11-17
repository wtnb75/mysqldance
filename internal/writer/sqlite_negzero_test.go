package writer

import (
	"io"
	"reflect"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// SQLite stores a whole-number value of a REAL column as an integer, which
// drops the sign of -0 (B-093), so the SQLite row writer says how many -0 each
// FLOAT / DOUBLE column held, once per column after the last row.
func TestSQLiteRowWriterWarnsOfNegativeZero(t *testing.T) {
	tbl := source.TableInfo{Name: "fp", Columns: []sqlvalue.Column{
		{Name: "id", TypeName: "INT"},
		{Name: "f", TypeName: "FLOAT"},
		{Name: "d", TypeName: "DOUBLE"},
		{Name: "u", TypeName: "UNSIGNED DOUBLE"},
	}}
	row := func(id, f, d, u string) []sqlvalue.Value {
		return []sqlvalue.Value{
			sqlvalue.NewValue("INT", []byte(id), false),
			sqlvalue.NewValue("FLOAT", []byte(f), f == ""),
			sqlvalue.NewValue("DOUBLE", []byte(d), false),
			sqlvalue.NewValue("UNSIGNED DOUBLE", []byte(u), false),
		}
	}
	rw, err := NewSQLite(datafile.Options{}).NewRowWriter("db", tbl, func() (io.WriteCloser, error) {
		return &fileBuffer{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range [][]sqlvalue.Value{
		row("1", "-0", "-0e0", "0"),
		row("2", "0", "-0.0", "0"),
		row("3", "-0.5", "-1e-300", "0"),
		row("4", "", "-0", "0"), // NULL and non-zero values are not -0
	} {
		if err := rw.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	w, ok := rw.(Warner)
	if !ok {
		t.Fatal("the SQLite row writer reports nothing")
	}
	want := []Warning{
		{Table: "fp", Message: "column `f` holds 1 value of -0, which SQLite stores as 0 (a REAL column drops the sign of -0)"},
		{Table: "fp", Message: "column `d` holds 3 values of -0, which SQLite stores as 0 (a REAL column drops the sign of -0)"},
	}
	if got := w.Warnings(); !reflect.DeepEqual(got, want) {
		t.Errorf("warnings = %q, want %q", got, want)
	}
}

func TestSQLiteRowWriterNoWarningWithoutNegativeZero(t *testing.T) {
	tbl := source.TableInfo{Name: "fp", Columns: []sqlvalue.Column{{Name: "d", TypeName: "DOUBLE"}}}
	rw, err := NewSQLite(datafile.Options{}).NewRowWriter("db", tbl, func() (io.WriteCloser, error) {
		return &fileBuffer{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.Write([]sqlvalue.Value{sqlvalue.NewValue("DOUBLE", []byte("0"), false)}); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rw.(Warner).Warnings(); len(got) != 0 {
		t.Errorf("warnings = %q, want none", got)
	}
}
