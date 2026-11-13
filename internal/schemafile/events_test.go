package schemafile

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
)

func TestWriteEvents(t *testing.T) {
	events := []Event{
		{Name: "purge", SQLMode: "STRICT_TRANS_TABLES", TimeZone: "+09:00",
			Create: "CREATE EVENT `purge` ON SCHEDULE EVERY 1 DAY STARTS '2030-01-01 00:00:00' ON COMPLETION NOT PRESERVE ENABLE DO DELETE FROM t"},
		{Name: "roll", SQLMode: "", TimeZone: "SYSTEM",
			Create: "CREATE EVENT `roll` ON SCHEDULE AT '2030-01-01 00:00:00' ON COMPLETION PRESERVE DISABLE DO BEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND"},
	}
	var buf bytes.Buffer
	if err := WriteEvents(&buf, "db", events); err != nil {
		t.Fatal(err)
	}
	// The time zone is set for each event: the server keeps the one the event
	// was created under, and the header's zone is not the source's.
	want := datafile.Header("db") + "DELIMITER $$\n" +
		"\nDROP EVENT IF EXISTS `purge`$$\n" +
		"SET SQL_MODE='STRICT_TRANS_TABLES'$$\n" +
		"SET TIME_ZONE='+09:00'$$\n" +
		"CREATE EVENT `purge` ON SCHEDULE EVERY 1 DAY STARTS '2030-01-01 00:00:00' ON COMPLETION NOT PRESERVE ENABLE DO DELETE FROM t$$\n" +
		"\nDROP EVENT IF EXISTS `roll`$$\n" +
		"SET SQL_MODE=''$$\n" +
		"SET TIME_ZONE='SYSTEM'$$\n" +
		"CREATE EVENT `roll` ON SCHEDULE AT '2030-01-01 00:00:00' ON COMPLETION PRESERVE DISABLE DO BEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND$$\n" +
		"\nDELIMITER ;\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteEvents =\n%q\nwant\n%q", got, want)
	}
}

func TestWriteEventsDelimiterAvoidsBody(t *testing.T) {
	events := []Event{{Name: "e", TimeZone: "SYSTEM",
		Create: "CREATE EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT '$$ and $$$'"}}
	var buf bytes.Buffer
	if err := WriteEvents(&buf, "db", events); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "DELIMITER //\n") {
		t.Errorf("delimiter was not changed to one the body lacks:\n%s", buf.String())
	}
}

func TestWriteEventsRefusesDelimiterLine(t *testing.T) {
	body := "CREATE EVENT `e` ON SCHEDULE EVERY 1 DAY DO BEGIN\nDELIMITER ;\nEND"
	var buf bytes.Buffer
	err := WriteEvents(&buf, "db", []Event{{Name: "e", TimeZone: "SYSTEM", Create: body}})
	if err == nil || !strings.Contains(err.Error(), "DELIMITER") {
		t.Errorf("WriteEvents error = %v, want one naming DELIMITER", err)
	}
}

// A missing time zone would make the event run in whatever zone the restore
// session has; refuse instead of guessing.
func TestWriteEventsRefusesNoTimeZone(t *testing.T) {
	var buf bytes.Buffer
	err := WriteEvents(&buf, "db", []Event{{Name: "e", Create: "CREATE EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1"}})
	if err == nil || !strings.Contains(err.Error(), "time zone") {
		t.Errorf("WriteEvents error = %v, want one naming the time zone", err)
	}
}

func TestWriteEventsDeterministic(t *testing.T) {
	events := []Event{{Name: "e", SQLMode: "ANSI", TimeZone: "+00:00", Create: "CREATE EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1"}}
	var a, b bytes.Buffer
	for _, w := range []*bytes.Buffer{&a, &b} {
		if err := WriteEvents(w, "db", events); err != nil {
			t.Fatal(err)
		}
	}
	if a.String() != b.String() {
		t.Error("output differs between runs")
	}
}

func TestStripDefinerEvent(t *testing.T) {
	in := "CREATE DEFINER=`root`@`%` EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 'DEFINER=`x`@`y`'"
	want := "CREATE EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 'DEFINER=`x`@`y`'"
	if got := StripDefiner(in); got != want {
		t.Errorf("StripDefiner(%q) = %q, want %q", in, got, want)
	}
}
