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

func TestStripDefinerKeepsBodyLiteral(t *testing.T) {
	in := "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS select 'DEFINER=`a`@`b` x' AS `c`"
	want := "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 'DEFINER=`a`@`b` x' AS `c`"
	if got := StripDefiner(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripDefinerQuotedUsers(t *testing.T) {
	cases := map[string]string{
		"CREATE DEFINER=`my-user`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1":      "CREATE SQL SECURITY DEFINER VIEW `v` AS select 1",
		"CREATE DEFINER=`we``ird@x`@`h` SQL SECURITY DEFINER VIEW `v` AS select 1":    "CREATE SQL SECURITY DEFINER VIEW `v` AS select 1",
		"CREATE OR REPLACE DEFINER=`u`@`h` SQL SECURITY INVOKER VIEW `v` AS select 1": "CREATE OR REPLACE SQL SECURITY INVOKER VIEW `v` AS select 1",
	}
	for in, want := range cases {
		if got := StripDefiner(in); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestStripDefinerNoHeaderDefiner(t *testing.T) {
	in := "CREATE VIEW `v` AS select 'DEFINER=`a`@`b`'"
	if got := StripDefiner(in); got != in {
		t.Errorf("got %q, want unchanged", got)
	}
}

// Real SHOW CREATE TABLE output of MySQL 8.0.46 and 8.4.13 (identical on
// both). Partition clauses follow the table-options line; a partition
// COMMENT is printed with a raw newline, so a later line can start with ")".
const (
	createRange = "CREATE TABLE `pr` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  `v` varchar(10) DEFAULT NULL,\n  PRIMARY KEY (`id`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=3 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='x)y'\n" +
		"/*!50100 PARTITION BY RANGE (`id`)\n(PARTITION p0 VALUES LESS THAN (10) COMMENT = 'a' ENGINE = InnoDB,\n" +
		" PARTITION p1 VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */"
	createHash = "CREATE TABLE `ph` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  PRIMARY KEY (`id`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=3 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci\n" +
		"/*!50100 PARTITION BY HASH (`id`)\nPARTITIONS 4 */"
	createKey = "CREATE TABLE `pk` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  PRIMARY KEY (`id`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci\n" +
		"/*!50100 PARTITION BY KEY (id)\nPARTITIONS 2 */"
	createSub = "CREATE TABLE `pl` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  `c` int NOT NULL,\n  PRIMARY KEY (`id`,`c`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci\n" +
		"/*!50100 PARTITION BY LIST (`c`)\nSUBPARTITION BY HASH (`id`)\nSUBPARTITIONS 2\n" +
		"(PARTITION a VALUES IN (1,2) ENGINE = InnoDB,\n PARTITION b VALUES IN (3) ENGINE = InnoDB) */"
	createNewlines = "CREATE TABLE `nl` (\n  `id` int NOT NULL AUTO_INCREMENT,\n" +
		"  `s` varchar(60) DEFAULT 'a\\n) AUTO_INCREMENT=5 x' COMMENT 'c\\n) AUTO_INCREMENT=6',\n" +
		"  `g` varchar(100) GENERATED ALWAYS AS (concat(`s`,_utf8mb4'q\\n) AUTO_INCREMENT=7')) VIRTUAL,\n" +
		"  `k` varchar(100) NOT NULL,\n  PRIMARY KEY (`id`,`k`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='tc\\n) AUTO_INCREMENT=8'\n" +
		"/*!50500 PARTITION BY LIST  COLUMNS(k)\n(PARTITION p0 VALUES IN ('x\\n) AUTO_INCREMENT=9') COMMENT = 'pc\n" +
		") AUTO_INCREMENT=10' ENGINE = InnoDB,\n PARTITION p1 VALUES IN ('y') ENGINE = InnoDB) */"
	createKeys = "CREATE TABLE `nl2` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  `s` varchar(60) DEFAULT NULL,\n" +
		"  `e` enum('a\\n) AUTO_INCREMENT=1','b') DEFAULT NULL,\n  PRIMARY KEY (`id`),\n" +
		"  KEY `ix` (`s`) COMMENT 'ic\\n) AUTO_INCREMENT=3',\n" +
		"  KEY `fx` ((concat(`s`,_utf8mb4'f\\n) AUTO_INCREMENT=4'))),\n" +
		"  CONSTRAINT `ck` CHECK ((`s` <> _latin1'z\\n) AUTO_INCREMENT=2'))\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci"
	// Identifiers are printed raw, newlines included.
	createRawIdent = "CREATE TABLE `n\n) AUTO_INCREMENT=1` (\n  `c\n) AUTO_INCREMENT=2` int NOT NULL AUTO_INCREMENT,\n" +
		"  PRIMARY KEY (`c\n) AUTO_INCREMENT=2`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci"
)

func TestStripAutoIncrementRealOutput(t *testing.T) {
	cases := map[string]struct{ in, option string }{
		"range":        {createRange, ") ENGINE=InnoDB AUTO_INCREMENT=3 "},
		"hash":         {createHash, ") ENGINE=InnoDB AUTO_INCREMENT=3 "},
		"key":          {createKey, ") ENGINE=InnoDB AUTO_INCREMENT=2 "},
		"subpartition": {createSub, ") ENGINE=InnoDB AUTO_INCREMENT=2 "},
		"newlines":     {createNewlines, ") ENGINE=InnoDB AUTO_INCREMENT=2 "},
		"keys":         {createKeys, ") ENGINE=InnoDB AUTO_INCREMENT=2 "},
		"raw ident":    {createRawIdent, ") ENGINE=InnoDB AUTO_INCREMENT=2 "},
	}
	for name, c := range cases {
		// Exactly the table option goes; every other "AUTO_INCREMENT=" text
		// (in strings, comments, identifiers, partitions) stays.
		if strings.Count(c.in, c.option) != 1 {
			t.Fatalf("%s: fixture must contain %q once", name, c.option)
		}
		want := strings.Replace(c.in, c.option, ") ENGINE=InnoDB ", 1)
		if got := StripAutoIncrement(c.in); got != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
}

func TestColumnNames(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		"range":     {createRange, []string{"id", "v"}},
		"newlines":  {createNewlines, []string{"id", "s", "g", "k"}},
		"keys":      {createKeys, []string{"id", "s", "e"}},
		"raw ident": {createRawIdent, []string{"c\n) AUTO_INCREMENT=2"}},
		"invisible": {"CREATE TABLE `cp` (\n  `id` int NOT NULL,\n  `a` int DEFAULT NULL,\n" +
			"  `g` int GENERATED ALWAYS AS ((`a` * 2)) VIRTUAL,\n  `h` int DEFAULT NULL /*!80023 INVISIBLE */,\n" +
			"  `secret` int DEFAULT NULL,\n  PRIMARY KEY (`id`)\n" +
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci", []string{"id", "a", "g", "h", "secret"}},
		"backticks": {"CREATE TABLE `t``x` (\n  `a``b` int,\n  `c,d` varchar(3) DEFAULT 'x,`y',\n" +
			"  FULLTEXT KEY `f` (`c,d`),\n  FOREIGN KEY (`a``b`) REFERENCES `o` (`id`)\n) ENGINE=InnoDB", []string{"a`b", "c,d"}},
	}
	for name, c := range cases {
		got, err := ColumnNames(c.in)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestColumnNamesMalformed(t *testing.T) {
	for _, in := range []string{"", "CREATE TABLE `t`", "CREATE TABLE `t` (\n  `a` int", "CREATE TABLE `t (a int)"} {
		if got, err := ColumnNames(in); err == nil {
			t.Errorf("ColumnNames(%q) = %q, want error", in, got)
		}
		// Unparsable input is returned unchanged, never mangled.
		if got := StripAutoIncrement(in); got != in {
			t.Errorf("StripAutoIncrement(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestStripAutoIncrementOnlyTableOptions(t *testing.T) {
	in := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT COMMENT 'x AUTO_INCREMENT=5',\n  `s` varchar(9) DEFAULT ' AUTO_INCREMENT=7',\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=42 DEFAULT CHARSET=utf8mb4"
	want := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT COMMENT 'x AUTO_INCREMENT=5',\n  `s` varchar(9) DEFAULT ' AUTO_INCREMENT=7',\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	if got := StripAutoIncrement(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAutoIncrementAtEnd(t *testing.T) {
	in := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB AUTO_INCREMENT=42"
	want := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB"
	if got := StripAutoIncrement(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAutoIncrementTableComment(t *testing.T) {
	in := "CREATE TABLE `t` (\n  `id` int\n) ENGINE=InnoDB AUTO_INCREMENT=3 COMMENT='keep AUTO_INCREMENT=9'"
	want := "CREATE TABLE `t` (\n  `id` int\n) ENGINE=InnoDB COMMENT='keep AUTO_INCREMENT=9'"
	if got := StripAutoIncrement(in); got != want {
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
	// SET NAMES (first in the settings) must precede CREATE DATABASE so a
	// non-ASCII database name is read as utf8mb4; USE follows it.
	prefix := datafile.HeaderSettings() + "\n" +
		"CREATE DATABASE IF NOT EXISTS `my``db` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;\n" +
		"USE `my``db`;\n"
	if !strings.HasPrefix(out, "SET NAMES utf8mb4;\n") || !strings.HasPrefix(out, prefix) {
		t.Errorf("unexpected prefix:\n got %q\nwant %q", out, prefix)
	}
	ia := strings.Index(out, "DROP TABLE IF EXISTS `a`;\nCREATE TABLE `a` (x int);")
	ib := strings.Index(out, "DROP TABLE IF EXISTS `b`;\nCREATE TABLE `b` (x int);")
	iv := strings.Index(out, "DROP VIEW IF EXISTS `v`;\nCREATE VIEW `v` AS select 1;")
	if ia < 0 || ib < 0 || iv < 0 || ia >= ib || ib >= iv {
		t.Errorf("bad ordering (a=%d b=%d v=%d): %q", ia, ib, iv, out)
	}
}
