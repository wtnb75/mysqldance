package schemafile

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
)

func TestWriteRoutines(t *testing.T) {
	routines := []Routine{
		{Name: "add_one", Type: "FUNCTION", SQLMode: "STRICT_TRANS_TABLES",
			Create: "CREATE FUNCTION `add_one`(x INT) RETURNS int\n    DETERMINISTIC\nRETURN x + 1"},
		{Name: "fill", Type: "PROCEDURE", SQLMode: "",
			Create: "CREATE PROCEDURE `fill`()\nBEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND"},
	}
	var buf bytes.Buffer
	if err := WriteRoutines(&buf, "db", routines); err != nil {
		t.Fatal(err)
	}
	want := datafile.Header("db") + "DELIMITER $$\n" +
		"\nDROP FUNCTION IF EXISTS `add_one`$$\n" +
		"SET SQL_MODE='STRICT_TRANS_TABLES'$$\n" +
		"CREATE FUNCTION `add_one`(x INT) RETURNS int\n    DETERMINISTIC\nRETURN x + 1$$\n" +
		"\nDROP PROCEDURE IF EXISTS `fill`$$\n" +
		"SET SQL_MODE=''$$\n" +
		"CREATE PROCEDURE `fill`()\nBEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND$$\n" +
		"\nDELIMITER ;\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteRoutines =\n%q\nwant\n%q", got, want)
	}
}

func TestWriteRoutinesDelimiterAvoidsBody(t *testing.T) {
	routines := []Routine{{Name: "p", Type: "PROCEDURE",
		Create: "CREATE PROCEDURE `p`() SELECT '$$ and $$$'"}}
	var buf bytes.Buffer
	if err := WriteRoutines(&buf, "db", routines); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "DELIMITER //\n") {
		t.Errorf("delimiter was not changed to one the body lacks:\n%s", out)
	}
}

func TestWriteRoutinesRefusesDelimiterLine(t *testing.T) {
	body := "CREATE PROCEDURE `p`()\nBEGIN\nDELIMITER ;\nEND"
	var buf bytes.Buffer
	err := WriteRoutines(&buf, "db", []Routine{{Name: "p", Type: "PROCEDURE", Create: body}})
	if err == nil || !strings.Contains(err.Error(), "DELIMITER") {
		t.Errorf("WriteRoutines error = %v, want one naming DELIMITER", err)
	}
}

func TestWriteRoutinesRefusesUnknownType(t *testing.T) {
	var buf bytes.Buffer
	err := WriteRoutines(&buf, "db", []Routine{{Name: "p", Type: "PACKAGE", Create: "CREATE PACKAGE `p` AS END"}})
	if err == nil || !strings.Contains(err.Error(), "PACKAGE") {
		t.Errorf("WriteRoutines error = %v, want one naming the type", err)
	}
}

func TestWriteRoutinesDeterministic(t *testing.T) {
	routines := []Routine{{Name: "p", Type: "PROCEDURE", SQLMode: "ANSI", Create: "CREATE PROCEDURE `p`() SET @x = 1"}}
	var a, b bytes.Buffer
	for _, w := range []*bytes.Buffer{&a, &b} {
		if err := WriteRoutines(w, "db", routines); err != nil {
			t.Fatal(err)
		}
	}
	if a.String() != b.String() {
		t.Error("output differs between runs")
	}
}

func TestStripDefinerRoutine(t *testing.T) {
	for in, want := range map[string]string{
		"CREATE DEFINER=`root`@`%` PROCEDURE `p`() SELECT 'DEFINER=`x`@`y`'": "CREATE PROCEDURE `p`() SELECT 'DEFINER=`x`@`y`'",
		"CREATE DEFINER=`root`@`%` FUNCTION `f`() RETURNS int RETURN 1":      "CREATE FUNCTION `f`() RETURNS int RETURN 1",
	} {
		if got := StripDefiner(in); got != want {
			t.Errorf("StripDefiner(%q) = %q, want %q", in, got, want)
		}
	}
}
