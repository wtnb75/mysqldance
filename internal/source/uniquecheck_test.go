package source

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/extsort"
	"mysqldance/internal/mask"
	"mysqldance/internal/sqlvalue"
)

// uniqueTable is a table (id, a, b) with a unique index uq on (a, b), or on a
// alone when only is true. fold says whether the collation of a ignores case.
func uniqueTable(fold, composite bool) TableInfo {
	uc := mask.UniqueCheck{Index: "uq", Columns: []string{"a"}, Positions: []int{1}, Fold: []bool{fold}}
	if composite {
		uc.Columns = []string{"a", "b"}
		uc.Positions = []int{1, 2}
		uc.Fold = []bool{fold, false}
	}
	return TableInfo{
		Name: "t",
		Columns: []sqlvalue.Column{
			{Name: "id", TypeName: "INT"}, {Name: "a", TypeName: "VARCHAR"}, {Name: "b", TypeName: "VARCHAR"},
		},
		OrderBy: []string{"id"},
		Unique:  []mask.UniqueCheck{uc},
	}
}

func uniqueRows(cells ...string) *fakeRows {
	r := &fakeRows{}
	for i := 0; i+2 < len(cells); i += 3 {
		row := make([][]byte, 3)
		for j := range row {
			if cells[i+j] != "<null>" {
				row[j] = []byte(cells[i+j])
			}
		}
		r.rows = append(r.rows, row)
	}
	return r
}

func runUnique(t *testing.T, s *Snapshot, ti TableInfo, rows *fakeRows) (int, error) {
	t.Helper()
	n := 0
	err := s.eachRowOf(rows, ti, func([]sqlvalue.Value) error { n++; return nil })
	return n, err
}

func TestUniqueCheckFindsCollisions(t *testing.T) {
	cases := []struct {
		name      string
		fold      bool
		composite bool
		rows      []string // id, a, b per row
		wantRows  int      // rows reported as colliding, 0 for none
	}{
		{"byte-equal values", false, false, []string{"1", "x", "-", "2", "x", "-", "3", "y", "-"}, 2},
		{"case differs, binary collation", false, false, []string{"1", "Abc", "-", "2", "abc", "-"}, 0},
		{"case differs, _ci collation", true, false, []string{"1", "Abc", "-", "2", "abc", "-"}, 2},
		{"three rows in a group", false, false, []string{"1", "x", "-", "2", "x", "-", "3", "x", "-"}, 3},
		{"two groups", false, false, []string{"1", "x", "-", "2", "x", "-", "3", "y", "-", "4", "y", "-"}, 4},
		{"NULL is not a collision", false, false, []string{"1", "<null>", "-", "2", "<null>", "-"}, 0},
		{"composite: same a, other b", false, true, []string{"1", "x", "p", "2", "x", "q"}, 0},
		{"composite: same pair", false, true, []string{"1", "x", "p", "2", "x", "p"}, 2},
		{"composite: NULL in one column", false, true, []string{"1", "x", "<null>", "2", "x", "<null>"}, 0},
		// The column boundary is part of the hash: ("ab","c") and ("a","bc") differ.
		{"composite: boundary", false, true, []string{"1", "ab", "c", "2", "a", "bc"}, 0},
		{"no collision", false, false, []string{"1", "x", "-", "2", "y", "-"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Snapshot{name: "shop", sortMemory: 1 << 20}
			_, err := runUnique(t, s, uniqueTable(c.fold, c.composite), uniqueRows(c.rows...))
			if c.wantRows == 0 {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a collision was not found")
			}
			msg := err.Error()
			for _, want := range []string{"`shop`.`t`", `"uq"`, "a", "collide"} {
				if !strings.Contains(msg, want) {
					t.Errorf("err %q lacks %q", msg, want)
				}
			}
			if want := " " + string(rune('0'+c.wantRows)) + " rows"; !strings.Contains(msg, want) {
				t.Errorf("err %q lacks %q", msg, want)
			}
			// Never a value.
			for _, v := range []string{"Abc", "abc", "ab"} {
				if strings.Contains(msg, "'"+v+"'") || strings.Contains(msg, "\""+v+"\"") {
					t.Errorf("err %q has the value %q", msg, v)
				}
			}
		})
	}
}

// Only hashes are held, in memory and in scratch files: a raw or a transformed
// value there would be a copy of the data. The table has no OrderBy, so the
// rows' own sort uses the scratch files too; use a keyed table to look at the
// check alone.
func TestUniqueCheckSpillsOnlyHashes(t *testing.T) {
	var files []*memFile
	s := &Snapshot{
		name:       "shop",
		sortMemory: 1, // every record is spilled
		sortFile: func() (extsort.File, error) {
			f := &memFile{}
			files = append(files, f)
			return f, nil
		},
	}
	rows := uniqueRows("1", "MASKEDVALUE-A", "-", "2", "MASKEDVALUE-B", "-", "3", "MASKEDVALUE-A", "-")
	_, err := runUnique(t, s, uniqueTable(false, false), rows)
	if err == nil || !strings.Contains(err.Error(), "2 rows") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "MASKEDVALUE") {
		t.Errorf("the error has a value: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("the check did not spill, so the test checks nothing")
	}
	for _, f := range files {
		if bytes.Contains(f.data, []byte("MASKEDVALUE")) {
			t.Errorf("a value was written to a scratch file: %q", f.data)
		}
	}
}

// A table without a unique check is read as before, and the rows still reach fn
// when there is no collision.
func TestUniqueCheckPassesRowsOn(t *testing.T) {
	s := &Snapshot{name: "shop", sortMemory: 1 << 20}
	n, err := runUnique(t, s, uniqueTable(false, false), uniqueRows("1", "x", "-", "2", "y", "-"))
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	ti := uniqueTable(false, false)
	ti.Unique = nil
	n, err = runUnique(t, s, ti, uniqueRows("1", "x", "-", "2", "x", "-"))
	if err != nil || n != 2 {
		t.Fatalf("without a check: n = %d, err = %v", n, err)
	}
}
