package datafile

import (
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

// benchRows builds n rows of already-rendered SQL literals, like the ones the
// writer receives from source.Rows.
func benchRows(n int) [][]string {
	rows := make([][]string, n)
	for i := range rows {
		rows[i] = []string{
			fmt.Sprint(i),
			fmt.Sprintf("'name-%d'", i),
			"NULL",
			fmt.Sprintf("'%d.25'", i%1000),
			"'2020-01-02 03:04:05'",
		}
	}
	return rows
}

var benchColumns = []string{"id", "name", "note", "price", "created"}

// BenchmarkWriteStatements writes 100,000 rows to a discarding writer.
func BenchmarkWriteStatements(b *testing.B) {
	rows := benchRows(100000)
	b.ReportAllocs()
	for b.Loop() {
		if err := WriteStatements(io.Discard, Header("d"), "", sqlvalue.QuoteIdent,
			"t", benchColumns, false, rows, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

// benchStream adds 100,000 rows to a Stream that cuts files by key hash, the
// path of a table written in several files.
func benchStream(b *testing.B, keyCols []int) {
	rows := benchRows(100000)
	b.ReportAllocs()
	for b.Loop() {
		s := NewStream(func() (io.WriteCloser, error) { return nopCloser{io.Discard}, nil },
			Header("d"), "", sqlvalue.QuoteIdent,
			"t", benchColumns, false, keyCols, Options{RowsPerFile: 10000})
		for _, row := range rows {
			if err := s.Add(row); err != nil {
				b.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStreamSplit(b *testing.B) { benchStream(b, []int{0}) }

// BenchmarkStreamSplitNoKey is for a table without a key, where the whole row
// is hashed.
func BenchmarkStreamSplitNoKey(b *testing.B) { benchStream(b, nil) }

// TestKeyHashMatchesFNV pins keyHash to hash/fnv: the allocation-free fnvAdd
// must not change where files are cut.
func TestKeyHashMatchesFNV(t *testing.T) {
	rows := [][]string{{}, {""}, {"1"}, {"1,2", "3"}, {"1", "2,3"}, {"NULL", "'é'", "x"}}
	for _, row := range rows {
		for _, keyCols := range [][]int{nil, {0}, {len(row) - 1}} {
			if len(row) == 0 && keyCols != nil {
				continue
			}
			h := fnv.New32a()
			if keyCols == nil {
				_, _ = h.Write([]byte(strings.Join(row, ",")))
			} else {
				for _, c := range keyCols {
					_, _ = h.Write([]byte(row[c]))
					_, _ = h.Write([]byte{0})
				}
			}
			if got, want := keyHash(row, keyCols), mix32(h.Sum32()); got != want {
				t.Errorf("keyHash(%q, %v) = %d, want %d", row, keyCols, got, want)
			}
		}
	}
}
