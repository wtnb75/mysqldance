package writer

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
)

func sqlSecuritySchema() Schema {
	return Schema{DB: "d",
		Views: []schemafile.View{{Name: "v", Create: "CREATE ALGORITHM=UNDEFINED DEFINER=`x`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1 AS `a`"}},
		Routines: []schemafile.Routine{
			{Name: "f", Type: "FUNCTION", Create: "CREATE DEFINER=`x`@`%` FUNCTION `f`() RETURNS int\n    DETERMINISTIC\nRETURN 1"},
			{Name: "g", Type: "FUNCTION", Create: "CREATE DEFINER=`x`@`%` FUNCTION `g`() RETURNS int\n    SQL SECURITY INVOKER\nRETURN 1"},
		},
	}
}

// --sql-security=invoker (B-112) rewrites the views and the routines, whether
// the DEFINER clause is kept or not.
func TestMySQLSQLSecurityInvoker(t *testing.T) {
	for _, keep := range []bool{false, true} {
		w := NewMySQL(datafile.Options{}).WithKeepDefiner(keep).WithSQLSecurityInvoker(true)
		var views, routines bytes.Buffer
		if err := w.WriteViews(&views, sqlSecuritySchema()); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteRoutines(&routines, sqlSecuritySchema()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(views.String(), "SQL SECURITY DEFINER") || !strings.Contains(views.String(), "SQL SECURITY INVOKER VIEW") {
			t.Errorf("keep=%v: view not rewritten:\n%s", keep, views.String())
		}
		if got := strings.Count(routines.String(), "SQL SECURITY INVOKER"); got != 2 {
			t.Errorf("keep=%v: %d SQL SECURITY INVOKER lines in the routines, want 2:\n%s", keep, got, routines.String())
		}
		if got := strings.Contains(routines.String(), "DEFINER=`x`"); got != keep {
			t.Errorf("keep=%v: definer present = %v", keep, got)
		}
	}
}

// The default leaves SQL SECURITY as SHOW CREATE returned it.
func TestMySQLSQLSecurityKeepByDefault(t *testing.T) {
	w := NewMySQL(datafile.Options{})
	var views, routines bytes.Buffer
	if err := w.WriteViews(&views, sqlSecuritySchema()); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRoutines(&routines, sqlSecuritySchema()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(views.String(), "SQL SECURITY DEFINER VIEW") {
		t.Errorf("view changed:\n%s", views.String())
	}
	if got := strings.Count(routines.String(), "SQL SECURITY INVOKER"); got != 1 {
		t.Errorf("%d SQL SECURITY INVOKER lines in the routines, want 1:\n%s", got, routines.String())
	}
}

// A routine whose signature cannot be read stops the dump.
func TestMySQLSQLSecurityInvokerRoutineError(t *testing.T) {
	w := NewMySQL(datafile.Options{}).WithSQLSecurityInvoker(true)
	err := w.WriteRoutines(&bytes.Buffer{}, Schema{DB: "d",
		Routines: []schemafile.Routine{{Name: "f", Type: "FUNCTION", Create: "CREATE FUNCTION `f`() RETURNS int RETURN 1"}}})
	if err == nil || !strings.Contains(err.Error(), "`f`") {
		t.Errorf("err = %v, want one that names the routine", err)
	}
}
