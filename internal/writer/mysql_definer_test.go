package writer

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
)

// With the definer kept, views, routines, events and triggers are written with
// the DEFINER clause exactly as SHOW CREATE returned it (B-111).
func TestMySQLKeepDefiner(t *testing.T) {
	w := NewMySQL(datafile.Options{}).WithKeepDefiner(true)
	s := Schema{DB: "d",
		Views:    []schemafile.View{{Name: "v", Create: "CREATE ALGORITHM=UNDEFINED DEFINER=`x`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1 AS `a`"}},
		Routines: []schemafile.Routine{{Name: "f", Type: "FUNCTION", Create: "CREATE DEFINER=`x`@`%` FUNCTION `f`() RETURNS int RETURN 1"}},
		Events:   []schemafile.Event{{Name: "e", TimeZone: "+09:00", Create: "CREATE DEFINER=`x`@`%` EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1"}},
		Triggers: []schemafile.Trigger{{Name: "t", Table: "a", Create: "CREATE DEFINER=`x`@`%` TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.x = 1"}},
	}
	for name, write := range map[string]func(*bytes.Buffer) error{
		"views":    func(b *bytes.Buffer) error { return w.WriteViews(b, s) },
		"routines": func(b *bytes.Buffer) error { return w.WriteRoutines(b, s) },
		"events":   func(b *bytes.Buffer) error { return w.WriteEvents(b, s) },
		"triggers": func(b *bytes.Buffer) error { return w.WriteTriggers(b, s) },
	} {
		var got bytes.Buffer
		if err := write(&got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(got.String(), "DEFINER=`x`@`%`") {
			t.Errorf("%s: DEFINER was removed:\n%s", name, got.String())
		}
	}
}

// The default still strips it.
func TestMySQLStripsDefinerByDefault(t *testing.T) {
	w := NewMySQL(datafile.Options{}).WithKeepDefiner(false)
	var got bytes.Buffer
	err := w.WriteRoutines(&got, Schema{DB: "d",
		Routines: []schemafile.Routine{{Name: "f", Type: "FUNCTION", Create: "CREATE DEFINER=`x`@`%` FUNCTION `f`() RETURNS int RETURN 1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.String(), "DEFINER") {
		t.Errorf("DEFINER left in %q", got.String())
	}
}
