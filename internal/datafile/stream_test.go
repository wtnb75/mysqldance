package datafile

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"testing"
)

// legacyWriteStatements is the in-memory implementation that Stream replaced,
// kept verbatim as the reference for the byte-for-byte comparison below.
func legacyWriteStatements(w io.Writer, header, footer string, quote func(string) string, table string, columns []string, listColumns bool, rows [][]string, opt Options) error {
	if opt.RowsPerInsert == 0 {
		opt.RowsPerInsert = defaultRowsPerInsert
	}
	if opt.MaxInsertBytes <= 0 {
		opt.MaxInsertBytes = defaultMaxInsertBytes
	}
	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString(header); err != nil {
		return err
	}
	var head strings.Builder
	head.WriteString("INSERT INTO " + quote(table))
	if listColumns {
		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = quote(c)
		}
		head.WriteString(" (" + strings.Join(quoted, ", ") + ")")
	}
	head.WriteString(" VALUES\n")
	open := false
	size := 0
	for i, row := range rows {
		if !open {
			_, _ = bw.WriteString(head.String())
			size = head.Len()
			open = true
		}
		text := "(" + strings.Join(row, ",") + ")"
		h := fnv.New32a()
		_, _ = h.Write([]byte(text))
		size += len(text) + 2
		closeNow := i == len(rows)-1 || h.Sum32()%opt.RowsPerInsert == 0 || size >= opt.MaxInsertBytes
		sep := ",\n"
		if closeNow {
			sep = ";\n"
		}
		_, _ = bw.WriteString(text + sep)
		if closeNow {
			open = false
		}
	}
	if _, err := bw.WriteString(footer); err != nil {
		return err
	}
	return bw.Flush()
}

// closeBuffer is a file that records whether it was closed.
type closeBuffer struct {
	bytes.Buffer
	closed bool
}

func (c *closeBuffer) Close() error { c.closed = true; return nil }

// streamFiles feeds rows to a Stream and returns the files it wrote.
func streamFiles(t *testing.T, rows [][]string, keyCols []int, listColumns bool, opt Options) []*closeBuffer {
	t.Helper()
	var files []*closeBuffer
	open := func() (io.WriteCloser, error) {
		f := &closeBuffer{}
		files = append(files, f)
		return f, nil
	}
	s := NewStream(open, "H;\n", "F;\n", func(s string) string { return "`" + s + "`" },
		"t", []string{"a", "b"}, listColumns, keyCols, opt)
	for _, row := range rows {
		if err := s.Add(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return files
}

// legacyFiles is what the old code wrote: Split, then one file per group.
func legacyFiles(t *testing.T, rows [][]string, keyCols []int, listColumns bool, opt Options) []string {
	t.Helper()
	var out []string
	for _, g := range Split(rows, keyCols, opt.RowsPerFile) {
		var buf bytes.Buffer
		err := legacyWriteStatements(&buf, "H;\n", "F;\n", func(s string) string { return "`" + s + "`" },
			"t", []string{"a", "b"}, listColumns, g, opt)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, buf.String())
	}
	return out
}

func TestStreamMatchesInMemoryOutput(t *testing.T) {
	cases := []struct {
		name string
		rows int
		opt  Options
		keys []int
	}{
		{"empty", 0, Options{}, []int{0}},
		{"empty split", 0, Options{RowsPerFile: 5}, []int{0}},
		{"one row", 1, Options{}, []int{0}},
		{"defaults", 5000, Options{}, []int{0}},
		{"small inserts", 300, Options{RowsPerInsert: 7}, []int{0}},
		{"one row per insert", 50, Options{RowsPerInsert: 1}, []int{0}},
		{"tiny max bytes", 100, Options{MaxInsertBytes: 40}, []int{0}},
		{"split", 1000, Options{RowsPerFile: 50, RowsPerInsert: 10}, []int{0}},
		{"split no key", 1000, Options{RowsPerFile: 50}, nil},
		{"split one row per file", 40, Options{RowsPerFile: 1}, []int{0}},
		{"split and tiny max bytes", 400, Options{RowsPerFile: 30, MaxInsertBytes: 60}, []int{0, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := makeRows(c.rows)
			for _, listColumns := range []bool{false, true} {
				want := legacyFiles(t, rows, c.keys, listColumns, c.opt)
				got := streamFiles(t, rows, c.keys, listColumns, c.opt)
				if len(got) != len(want) {
					t.Fatalf("%d files, want %d", len(got), len(want))
				}
				for i := range want {
					if got[i].String() != want[i] {
						t.Fatalf("file %d differs:\n%s\nwant:\n%s", i, got[i].String(), want[i])
					}
					if !got[i].closed {
						t.Errorf("file %d was not closed", i)
					}
				}
			}
		})
	}
}

// The last row landing on a file boundary must not leave a trailing empty file.
func TestStreamLastRowOnFileBoundary(t *testing.T) {
	rows := makeRows(300)
	opt := Options{RowsPerFile: 3}
	for n := 1; n <= len(rows); n++ {
		want := legacyFiles(t, rows[:n], []int{0}, false, opt)
		got := streamFiles(t, rows[:n], []int{0}, false, opt)
		if len(got) != len(want) {
			t.Fatalf("%d rows: %d files, want %d", n, len(got), len(want))
		}
	}
}

func TestStreamEmptyTableStillWritesOneFile(t *testing.T) {
	got := streamFiles(t, nil, nil, false, Options{RowsPerFile: 10})
	if len(got) != 1 || got[0].String() != "H;\nF;\n" {
		t.Fatalf("got %d files: %q", len(got), got)
	}
}

func TestStreamPropagatesWriteErrors(t *testing.T) {
	boom := errors.New("boom")
	open := func() (io.WriteCloser, error) { return nil, boom }
	s := NewStream(open, "H;\n", "", func(s string) string { return s }, "t", nil, false, nil, Options{})
	err := s.Add([]string{"1"})
	if err == nil {
		err = s.Close()
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestStreamDoesNotHoldRows(t *testing.T) {
	// Rows are written as they arrive: after many rows only a bounded buffer is
	// pending, so the file must already have received data well before Close.
	var f *closeBuffer
	open := func() (io.WriteCloser, error) { f = &closeBuffer{}; return f, nil }
	s := NewStream(open, "H;\n", "", func(s string) string { return s }, "t", []string{"a"}, false, nil, Options{})
	for i := range 100000 {
		if err := s.Add([]string{fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if f.Len() < 100000 {
		t.Fatalf("only %d bytes written before Close", f.Len())
	}
	_ = s.Close()
}
