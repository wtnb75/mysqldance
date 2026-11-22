package source

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	"mysqldance/internal/extsort"
	"mysqldance/internal/sqlvalue"
)

// memFile is a scratch file held in memory; it keeps every byte written to it.
type memFile struct {
	data []byte
	pos  int64
}

func (f *memFile) Write(p []byte) (int, error) {
	end := f.pos + int64(len(p))
	if end > int64(len(f.data)) {
		f.data = append(f.data, make([]byte, end-int64(len(f.data)))...)
	}
	copy(f.data[f.pos:], p)
	f.pos = end
	return len(p), nil
}

func (f *memFile) Read(p []byte) (int, error) {
	if f.pos >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.pos:])
	f.pos += int64(n)
	return n, nil
}

func (f *memFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.pos = off
	case io.SeekCurrent:
		f.pos += off
	default:
		f.pos = int64(len(f.data)) + off
	}
	return f.pos, nil
}

func (f *memFile) Remove() error { return nil }

// fakeRows serves rows of text cells; a nil cell is NULL.
type fakeRows struct {
	rows [][][]byte
	at   int
}

func (r *fakeRows) Next() bool { r.at++; return r.at <= len(r.rows) }
func (r *fakeRows) Err() error { return nil }
func (r *fakeRows) Scan(dest ...any) error {
	for i, d := range dest {
		*(d.(*sql.RawBytes)) = r.rows[r.at-1][i]
	}
	return nil
}

func keylessTable(mask func([]sqlvalue.Value) error) TableInfo {
	return TableInfo{
		Name:    "t",
		Columns: []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: "secret", TypeName: "VARCHAR"}},
		Mask:    mask,
	}
}

// replaceSecret is a mask that puts a fixed text where the secret column is.
func replaceSecret(text string) func([]sqlvalue.Value) error {
	return func(row []sqlvalue.Value) error {
		row[1] = sqlvalue.NewValue("VARCHAR", []byte(text), false)
		return nil
	}
}

// A table without a key is sorted before it is passed on. The rows the sort
// holds must already be masked: the scratch files of the sort lie in the output
// directory, so a raw value there would be a copy of the data that is not
// masked, and the order it is played back in would follow the raw values.
func TestEachRowMasksBeforeTheSort(t *testing.T) {
	var files []*memFile
	s := &Snapshot{
		sortMemory: 1, // every row is spilled
		sortFile: func() (extsort.File, error) {
			f := &memFile{}
			files = append(files, f)
			return f, nil
		},
	}
	// In the raw data the secrets sort z, a, m; masked, all rows are equal and
	// the order falls back to the id.
	rows := &fakeRows{rows: [][][]byte{
		{[]byte("3"), []byte("ZRAWSECRET")},
		{[]byte("1"), []byte("ARAWSECRET")},
		{[]byte("2"), []byte("MRAWSECRET")},
	}}
	var got []string
	err := s.eachRowOf(rows, keylessTable(replaceSecret("masked")), func(row []sqlvalue.Value) error {
		got = append(got, strings.Join(sqlvalue.Literals(row), ","))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("the sort did not spill, so the test checks nothing")
	}
	for _, f := range files {
		if bytes.Contains(f.data, []byte("RAWSECRET")) {
			t.Errorf("a raw value was written to a scratch file: %q", f.data)
		}
	}
	want := []string{"1,'masked'", "2,'masked'", "3,'masked'"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("rows = %v, want %v (the order must follow the masked text)", got, want)
	}
}

func TestEachRowWithoutMaskKeepsRows(t *testing.T) {
	s := &Snapshot{sortMemory: 1 << 20, sortFile: func() (extsort.File, error) { return &memFile{}, nil }}
	rows := &fakeRows{rows: [][][]byte{{[]byte("2"), []byte("b")}, {[]byte("1"), nil}}}
	var got []string
	err := s.eachRowOf(rows, keylessTable(nil), func(row []sqlvalue.Value) error {
		got = append(got, strings.Join(sqlvalue.Literals(row), ","))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "1,NULL;2,'b'"; strings.Join(got, ";") != want {
		t.Errorf("rows = %v, want %s", got, want)
	}
}

// A keyed table is streamed: the rows reach fn in the order of the query, masked.
func TestEachRowMasksAKeyedTable(t *testing.T) {
	s := &Snapshot{sortMemory: 1 << 20}
	ti := keylessTable(replaceSecret("x"))
	ti.OrderBy = []string{"id"}
	rows := &fakeRows{rows: [][][]byte{{[]byte("2"), []byte("b")}, {[]byte("1"), []byte("a")}}}
	var got []string
	err := s.eachRowOf(rows, ti, func(row []sqlvalue.Value) error {
		got = append(got, strings.Join(sqlvalue.Literals(row), ","))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "2,'x';1,'x'"; strings.Join(got, ";") != want {
		t.Errorf("rows = %v, want %s", got, want)
	}
}

// A failing mask stops the read; the error names the table, not a value.
func TestEachRowMaskErrorStopsTheRead(t *testing.T) {
	s := &Snapshot{sortMemory: 1 << 20}
	boom := errors.New("column \"secret\" cannot be masked")
	ti := keylessTable(func([]sqlvalue.Value) error { return boom })
	ti.OrderBy = []string{"id"}
	called := false
	err := s.eachRowOf(&fakeRows{rows: [][][]byte{{[]byte("1"), []byte("RAWSECRET")}}}, ti, func([]sqlvalue.Value) error {
		called = true
		return nil
	})
	if !errors.Is(err, boom) || strings.Contains(err.Error(), "RAWSECRET") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Error("a row that failed its mask was passed on")
	}
}
