package dump

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"mysqldance/internal/sqlvalue"
)

// With SQLSecurityInvoker, a view written with SQL SECURITY DEFINER and
// routines that write no SQL SECURITY restore as INVOKER, and the output is
// deterministic. Without it they restore as DEFINER (B-112).
func TestSQLSecurityInvokerRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE SQL SECURITY DEFINER VIEW v AS SELECT id FROM a",
		"CREATE FUNCTION f() RETURNS INT DETERMINISTIC RETURN 1",
		"CREATE PROCEDURE p() SELECT 1",
		"CREATE FUNCTION g() RETURNS INT DETERMINISTIC SQL SECURITY INVOKER RETURN 2",
	)

	dump := func(dir string, invoker bool) {
		t.Helper()
		var log bytes.Buffer
		err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log), SQLSecurityInvoker: invoker})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	securityTypes := func() (views, routines map[string]string) {
		t.Helper()
		query := func(q string) map[string]string {
			rows, err := e.admin.QueryContext(context.Background(), q, db)
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			defer func() {
				if err := rows.Close(); err != nil {
					t.Error(err)
				}
			}()
			got := map[string]string{}
			for rows.Next() {
				var name, typ string
				if err := rows.Scan(&name, &typ); err != nil {
					t.Fatal(err)
				}
				got[name] = typ
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return got
		}
		return query("SELECT TABLE_NAME, SECURITY_TYPE FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ?"),
			query("SELECT ROUTINE_NAME, SECURITY_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?")
	}

	for _, tc := range []struct {
		invoker  bool
		view     string
		routines map[string]string
	}{
		{false, "DEFINER", map[string]string{"f": "DEFINER", "p": "DEFINER", "g": "INVOKER"}},
		{true, "INVOKER", map[string]string{"f": "INVOKER", "p": "INVOKER", "g": "INVOKER"}},
	} {
		dir, again := t.TempDir(), t.TempDir()
		dump(dir, tc.invoker)
		dump(again, tc.invoker)
		if !reflect.DeepEqual(readFiles(t, dir), readFiles(t, again)) {
			t.Errorf("invoker=%v: two dumps of the same database differ", tc.invoker)
		}
		e.exec(t, "DROP DATABASE "+sqlvalue.QuoteIdent(db))
		e.restore(t, dir)
		views, routines := securityTypes()
		if !reflect.DeepEqual(views, map[string]string{"v": tc.view}) {
			t.Errorf("invoker=%v: views = %v, want v: %s", tc.invoker, views, tc.view)
		}
		if !reflect.DeepEqual(routines, tc.routines) {
			t.Errorf("invoker=%v: routines = %v, want %v", tc.invoker, routines, tc.routines)
		}
	}
}
