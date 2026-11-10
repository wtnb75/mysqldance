package schemafile

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
)

func TestWriteTriggers(t *testing.T) {
	triggers := []Trigger{
		{Name: "a_ins", SQLMode: "STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION",
			Create: "CREATE TRIGGER `a_ins` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.x = 1"},
		{Name: "b_upd", SQLMode: "",
			Create: "CREATE TRIGGER `b_upd` AFTER UPDATE ON `b` FOR EACH ROW BEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND"},
	}
	var buf bytes.Buffer
	if err := WriteTriggers(&buf, "db", triggers); err != nil {
		t.Fatal(err)
	}
	want := datafile.Header("db") + "DELIMITER $$\n" +
		"\nDROP TRIGGER IF EXISTS `a_ins`$$\n" +
		"SET SQL_MODE='STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION'$$\n" +
		"CREATE TRIGGER `a_ins` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.x = 1$$\n" +
		"\nDROP TRIGGER IF EXISTS `b_upd`$$\n" +
		"SET SQL_MODE=''$$\n" +
		"CREATE TRIGGER `b_upd` AFTER UPDATE ON `b` FOR EACH ROW BEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND$$\n" +
		"\nDELIMITER ;\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteTriggers =\n%q\nwant\n%q", got, want)
	}
}

func TestWriteTriggersDelimiterAvoidsBody(t *testing.T) {
	// A body that holds the usual delimiter must not end the statement early.
	triggers := []Trigger{{Name: "t",
		Create: "CREATE TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.s = '$$ and $$$'"}}
	var buf bytes.Buffer
	if err := WriteTriggers(&buf, "db", triggers); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "DELIMITER //\n") {
		t.Errorf("delimiter was not changed to one the body lacks:\n%s", out)
	}
	if !strings.Contains(out, "'$$ and $$$'") {
		t.Errorf("body changed:\n%s", out)
	}
}

// A statement that ends with the delimiter's character would otherwise swallow
// the first character of the delimiter, and leave a stray one for the next
// statement: `SET @a$` + `$$` is `SET @a$$$`, which a client cuts at the first `$$`.
func TestWriteTriggersDelimiterNotGluedToBody(t *testing.T) {
	triggers := []Trigger{{Name: "t", Create: "CREATE TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET @a$"}}
	var buf bytes.Buffer
	if err := WriteTriggers(&buf, "db", triggers); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	delim := strings.TrimSpace(strings.SplitN(strings.SplitN(out, "DELIMITER ", 2)[1], "\n", 2)[0])
	// Cut like the client does: at the first delimiter.
	rest := strings.SplitN(out, "DELIMITER "+delim+"\n", 2)[1]
	stmts := strings.Split(rest, delim)
	for _, want := range []string{"\nDROP TRIGGER IF EXISTS `t`", "\nSET SQL_MODE=''", "\nCREATE TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET @a$"} {
		found := false
		for _, s := range stmts {
			found = found || strings.TrimLeft(s, "\n") == strings.TrimLeft(want, "\n")
		}
		if !found {
			t.Errorf("statement %q does not survive a cut at %q:\n%q", want, delim, stmts)
		}
	}
}

func TestWriteTriggersRefusesDelimiterLine(t *testing.T) {
	for _, body := range []string{
		"CREATE TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW BEGIN\nDELIMITER ;\nEND",
		"CREATE TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW BEGIN\n  delimiter //\nEND",
	} {
		var buf bytes.Buffer
		err := WriteTriggers(&buf, "db", []Trigger{{Name: "t", Create: body}})
		if err == nil || !strings.Contains(err.Error(), "DELIMITER") {
			t.Errorf("WriteTriggers(%q) error = %v, want one naming DELIMITER", body, err)
		}
	}
}

func TestWriteTriggersDeterministic(t *testing.T) {
	triggers := []Trigger{{Name: "t", SQLMode: "ANSI", Create: "CREATE TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET @x = 1"}}
	var a, b bytes.Buffer
	for _, w := range []*bytes.Buffer{&a, &b} {
		if err := WriteTriggers(w, "db", triggers); err != nil {
			t.Fatal(err)
		}
	}
	if a.String() != b.String() {
		t.Error("output differs between runs")
	}
}

func TestStripDefinerTrigger(t *testing.T) {
	in := "CREATE DEFINER=`root`@`%` TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.s = 'DEFINER=`x`@`y`'"
	want := "CREATE TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.s = 'DEFINER=`x`@`y`'"
	if got := StripDefiner(in); got != want {
		t.Errorf("StripDefiner = %q, want %q", got, want)
	}
}
