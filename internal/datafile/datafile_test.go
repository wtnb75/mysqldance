package datafile

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func write(t *testing.T, rows [][]string, listColumns bool, opt Options) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, "db", "t", []string{"a", "b"}, listColumns, rows, opt); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func makeRows(n int) [][]string {
	rows := make([][]string, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, []string{fmt.Sprint(i), fmt.Sprintf("'row %d'", i)})
	}
	return rows
}

func TestWriteOneRowPerLine(t *testing.T) {
	rows := [][]string{{"1", "'a'"}, {"2", "'b'"}, {"3", "'c'"}}
	got := write(t, rows, false, Options{})
	want := Header("db") + "INSERT INTO `t` VALUES\n(1,'a'),\n(2,'b'),\n(3,'c');\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteEmptyTableWritesHeaderOnly(t *testing.T) {
	got := write(t, nil, false, Options{})
	if got != Header("db") {
		t.Fatalf("got %q", got)
	}
}

func TestHeaderQuotesDatabaseName(t *testing.T) {
	want := "SET NAMES utf8mb4;\n" +
		"SET TIME_ZONE='+00:00';\n" +
		"SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO';\n" +
		"SET FOREIGN_KEY_CHECKS=0;\n" +
		"SET UNIQUE_CHECKS=0;\n" +
		"USE `a``b`;\n"
	if got := Header("a`b"); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestWriteListColumns(t *testing.T) {
	got := write(t, [][]string{{"1", "'a'"}}, true, Options{})
	want := Header("db") + "INSERT INTO `t` (`a`, `b`) VALUES\n(1,'a');\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func diffLines(a, b string) int {
	x := strings.Split(a, "\n")
	y := strings.Split(b, "\n")
	p := 0
	for p < len(x) && p < len(y) && x[p] == y[p] {
		p++
	}
	s := 0
	for s < len(x)-p && s < len(y)-p && x[len(x)-1-s] == y[len(y)-1-s] {
		s++
	}
	return max(len(x)-p-s, len(y)-p-s)
}

func TestChunkBoundariesAreLocal(t *testing.T) {
	opt := Options{RowsPerInsert: 50}
	rows := makeRows(5000)
	base := write(t, rows, false, opt)
	worst := 0
	for i := 1000; i < 1300; i++ {
		less := make([][]string, 0, len(rows)-1)
		less = append(less, rows[:i]...)
		less = append(less, rows[i+1:]...)
		got := diffLines(base, write(t, less, false, opt))
		if got > worst {
			worst = got
		}
		if got > 6 {
			t.Fatalf("deleting row %d changed %d lines", i, got)
		}
	}
	t.Logf("worst-case changed lines: %d", worst)
}

func TestMaxInsertBytes(t *testing.T) {
	const limit = 200
	out := write(t, makeRows(500), false, Options{RowsPerInsert: 1 << 30, MaxInsertBytes: limit})
	var stmts []string
	for _, s := range strings.Split(out, "INSERT INTO")[1:] {
		stmts = append(stmts, "INSERT INTO"+s)
	}
	if len(stmts) < 2 {
		t.Fatalf("expected several statements, got %d", len(stmts))
	}
	// Allow one row of slack, since a statement closes after the row that crosses the limit.
	for _, s := range stmts {
		if len(s) > limit+40 {
			t.Fatalf("statement of %d bytes exceeds limit: %q", len(s), s)
		}
	}
}

func TestDeterministic(t *testing.T) {
	rows := makeRows(3000)
	a := write(t, rows, true, Options{})
	b := write(t, rows, true, Options{})
	if a != b {
		t.Fatal("output differs between runs")
	}
}
