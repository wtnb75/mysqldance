package schemafile

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
)

func TestStripAutoIncrement(t *testing.T) {
	in := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=42 DEFAULT CHARSET=utf8mb4"
	want := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	if got := StripAutoIncrement(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAutoIncrementKeepsColumnKeyword(t *testing.T) {
	in := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB"
	if got := StripAutoIncrement(in); got != in {
		t.Errorf("got %q, want unchanged", got)
	}
}

func TestStripDefiner(t *testing.T) {
	in := "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1"
	want := "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1"
	if got := StripDefiner(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOrderViews(t *testing.T) {
	deps := map[string][]string{
		"c": {"a", "base_table"},
		"a": {"b"},
	}
	want := []string{"b", "a", "c", "x"}
	for _, in := range [][]string{{"c", "a", "b", "x"}, {"x", "b", "c", "a"}, {"a", "x", "c", "b"}} {
		if got := OrderViews(in, deps); !reflect.DeepEqual(got, want) {
			t.Errorf("OrderViews(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestOrderViewsCycle(t *testing.T) {
	deps := map[string][]string{"a": {"b"}, "b": {"a"}}
	got := OrderViews([]string{"b", "a", "c"}, deps)
	want := []string{"c", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestWrite(t *testing.T) {
	var buf bytes.Buffer
	err := Write(&buf, "my`db",
		"CREATE DATABASE `my``db` /*!40100 DEFAULT CHARACTER SET utf8mb4 */",
		[]Table{{"b", "CREATE TABLE `b` (x int)"}, {"a", "CREATE TABLE `a` (x int)"}},
		[]View{{"v", "CREATE VIEW `v` AS select 1"}})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "CREATE DATABASE IF NOT EXISTS `my``db` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;\n") {
		t.Errorf("unexpected prefix: %q", out)
	}
	if !strings.Contains(out, datafile.Header("my`db")) {
		t.Errorf("header missing: %q", out)
	}
	ia := strings.Index(out, "DROP TABLE IF EXISTS `a`;\nCREATE TABLE `a` (x int);")
	ib := strings.Index(out, "DROP TABLE IF EXISTS `b`;\nCREATE TABLE `b` (x int);")
	iv := strings.Index(out, "DROP VIEW IF EXISTS `v`;\nCREATE VIEW `v` AS select 1;")
	if ia < 0 || ib < 0 || iv < 0 || ia >= ib || ib >= iv {
		t.Errorf("bad ordering (a=%d b=%d v=%d): %q", ia, ib, iv, out)
	}
}
