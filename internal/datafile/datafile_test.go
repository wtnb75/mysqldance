package datafile

import (
	"bytes"
	"fmt"
	"reflect"
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
	for i := range n {
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

func TestWriteStatementsUsesTheGivenQuoting(t *testing.T) {
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	var buf bytes.Buffer
	err := WriteStatements(&buf, "BEGIN;\n", "COMMIT;\n", quote, `t"1`, []string{"a", "b"}, true,
		[][]string{{"1", "'x'"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := "BEGIN;\nINSERT INTO \"t\"\"1\" (\"a\", \"b\") VALUES\n(1,'x');\nCOMMIT;\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
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

func flatten(groups [][][]string) [][]string {
	var out [][]string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func TestSplitDisabledKeepsOneGroup(t *testing.T) {
	rows := makeRows(100)
	got := Split(rows, []int{0}, 0)
	if len(got) != 1 || len(got[0]) != len(rows) {
		t.Fatalf("got %d groups, want 1 holding every row", len(got))
	}
}

func TestSplitEmptyTableIsOneEmptyGroup(t *testing.T) {
	got := Split(nil, []int{0}, 10)
	if len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("got %v, want one empty group", got)
	}
}

func TestSplitKeepsRowsInOrder(t *testing.T) {
	rows := makeRows(3000)
	groups := Split(rows, []int{0}, 100)
	if len(groups) < 10 {
		t.Fatalf("got %d groups for 3000 rows at ~100 per group", len(groups))
	}
	if !reflect.DeepEqual(flatten(groups), rows) {
		t.Fatal("concatenated groups differ from the input")
	}
	for i, g := range groups {
		if len(g) == 0 {
			t.Fatalf("group %d is empty", i)
		}
	}
}

// Updating a non-key column must not move any boundary, so the other groups
// stay as they were.
func TestSplitBoundariesIgnoreNonKeyColumns(t *testing.T) {
	rows := makeRows(3000)
	base := Split(rows, []int{0}, 100)
	for i := 0; i < len(rows); i += 7 {
		changed := make([][]string, len(rows))
		copy(changed, rows)
		changed[i] = []string{rows[i][0], "'changed'"}
		got := Split(changed, []int{0}, 100)
		if len(got) != len(base) {
			t.Fatalf("updating row %d changed the group count: %d -> %d", i, len(base), len(got))
		}
		for g := range base {
			if len(got[g]) != len(base[g]) {
				t.Fatalf("updating row %d moved a boundary in group %d", i, g)
			}
		}
	}
}

// Deleting a row only affects the group it was in (and at most the next one).
func TestSplitDeleteIsLocal(t *testing.T) {
	rows := makeRows(3000)
	base := Split(rows, []int{0}, 100)
	less := append(append([][]string{}, rows[:1500]...), rows[1501:]...)
	got := Split(less, []int{0}, 100)
	same := 0
	seen := map[string]bool{}
	for _, g := range base {
		seen[fmt.Sprint(g)] = true
	}
	for _, g := range got {
		if seen[fmt.Sprint(g)] {
			same++
		}
	}
	if changed := len(got) - same; changed > 2 {
		t.Fatalf("deleting one row changed %d groups", changed)
	}
}

// Without a key the whole row is hashed.
func TestSplitWithoutKeyHashesTheRow(t *testing.T) {
	rows := makeRows(3000)
	groups := Split(rows, nil, 100)
	if len(groups) < 10 || !reflect.DeepEqual(flatten(groups), rows) {
		t.Fatalf("got %d groups, or rows lost", len(groups))
	}
}

func TestSplitIsDeterministic(t *testing.T) {
	rows := makeRows(3000)
	if !reflect.DeepEqual(Split(rows, []int{0}, 100), Split(rows, []int{0}, 100)) {
		t.Fatal("Split differs between calls")
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
