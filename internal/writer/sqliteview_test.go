package writer

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

const viewHead = "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v` AS "

func TestConvertSQLiteView(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string   // the converted statement; "" when the view is not converted
		caution []string // the reasons the result may differ from MySQL
		reason  string   // a substring of the reason the view is not converted
	}{
		{name: "plain select",
			body: "select `t`.`a` AS `a`,count(0) AS `n` from `t` where (`t`.`a` > 1)",
			want: "CREATE VIEW `v` AS select `t`.`a` AS `a`,count(0) AS `n` from `t` where (`t`.`a` > 1);\n"},
		{name: "join",
			body: "select `a`.`id` AS `id`,`b`.`x` AS `x` from (`a` join `b` on((`a`.`id` = `b`.`id`))) left join `c` on((`c`.`id` = `a`.`id`))",
			want: "CREATE VIEW `v` AS select `a`.`id` AS `id`,`b`.`x` AS `x` from (`a` join `b` on((`a`.`id` = `b`.`id`))) left join `c` on((`c`.`id` = `a`.`id`));\n"},
		{name: "introducer is dropped and a string is requoted",
			body:    "select concat(`t`.`b`,_utf8mb4' it\\'s') AS `c` from `t`",
			want:    "CREATE VIEW `v` AS select (`t`.`b` || ' it''s') AS `c` from `t`;\n",
			caution: []string{"a string literal"}},
		{name: "if becomes case",
			body:    "select if((`t`.`a` > 1),_utf8mb4'big',_utf8mb4'small') AS `s` from `t`",
			want:    "CREATE VIEW `v` AS select CASE WHEN (`t`.`a` > 1) THEN 'big' ELSE 'small' END AS `s` from `t`;\n",
			caution: []string{"a string literal"}},
		{name: "nested rewrites",
			body:    "select concat(if((`t`.`a` > 1),`t`.`b`,_utf8mb4'-'),_utf8mb4'!') AS `s` from `t`",
			want:    "CREATE VIEW `v` AS select (CASE WHEN (`t`.`a` > 1) THEN `t`.`b` ELSE '-' END || '!') AS `s` from `t`;\n",
			caution: []string{"a string literal"}},
		// || binds tighter than + and = in SQLite, so each argument keeps its own parentheses.
		{name: "concat keeps the precedence of its arguments",
			body: "select concat((`t`.`a` + 1),`t`.`b`,(`t`.`a` = 2)) AS `k` from `t`",
			want: "CREATE VIEW `v` AS select ((`t`.`a` + 1) || `t`.`b` || (`t`.`a` = 2)) AS `k` from `t`;\n"},
		{name: "concat wraps an argument that is not one token",
			body: "select concat(`t`.`a` + 1,`t`.`b`) AS `k` from `t`",
			want: "CREATE VIEW `v` AS select ((`t`.`a` + 1) || `t`.`b`) AS `k` from `t`;\n"},
		{name: "count distinct with two columns",
			body:   "select count(distinct `t`.`a`,`t`.`b`) AS `n` from `t`",
			reason: "COUNT(DISTINCT"},
		{name: "substr is cautioned",
			body:    "select substr(`t`.`b`,1,2) AS `s` from `t`",
			want:    "CREATE VIEW `v` AS select substr(`t`.`b`,1,2) AS `s` from `t`;\n",
			caution: []string{"SUBSTR()"}},
		{name: "two minus signs never form a comment",
			body: "select (`t`.`a` - -(1)) AS `k` from `t`",
			want: "CREATE VIEW `v` AS select (`t`.`a` - -(1)) AS `k` from `t`;\n"},
		{name: "adjacent minus signs are separated",
			body: "select (`t`.`a`--1) AS `k` from `t`",
			want: "CREATE VIEW `v` AS select (`t`.`a`- -1) AS `k` from `t`;\n"},
		{name: "unknown function inside a rewrite",
			body:   "select if(isnull(`t`.`a`),0,1) AS `s` from `t`",
			reason: "ISNULL"},
		{name: "ifnull and coalesce pass",
			body: "select ifnull(`t`.`a`,0) AS `x`,coalesce(`t`.`a`,`t`.`b`,0) AS `y`,nullif(`t`.`a`,0) AS `z`,abs(`t`.`a`) AS `w` from `t`",
			want: "CREATE VIEW `v` AS select ifnull(`t`.`a`,0) AS `x`,coalesce(`t`.`a`,`t`.`b`,0) AS `y`,nullif(`t`.`a`,0) AS `z`,abs(`t`.`a`) AS `w` from `t`;\n"},
		{name: "case expression passes",
			body: "select (case when (`t`.`a` is null) then 0 when (`t`.`a` in (1,2)) then 1 else 2 end) AS `c` from `t`",
			want: "CREATE VIEW `v` AS select (case when (`t`.`a` is null) then 0 when (`t`.`a` in (1,2)) then 1 else 2 end) AS `c` from `t`;\n"},
		{name: "division and group by are cautioned",
			body:    "select `t`.`g` AS `g`,(sum(`t`.`a`) / 2) AS `h` from `t` group by `t`.`g` order by `t`.`g` limit 5,10",
			want:    "CREATE VIEW `v` AS select `t`.`g` AS `g`,(sum(`t`.`a`) / 2) AS `h` from `t` group by `t`.`g` order by `t`.`g` limit 5,10;\n",
			caution: []string{"a division", "GROUP BY", "ORDER BY", "SUM()"}},
		{name: "subquery passes through the same rules",
			body: "select `t`.`a` AS `a` from `t` where `t`.`a` in (select `u`.`a` from `u`)",
			want: "CREATE VIEW `v` AS select `t`.`a` AS `a` from `t` where `t`.`a` in (select `u`.`a` from `u`);\n"},
		{name: "unknown function",
			body:   "select date_format(`t`.`d`,_utf8mb4'%Y') AS `y` from `t`",
			reason: "DATE_FORMAT",
		},
		{name: "check option",
			body:   "select `t`.`a` AS `a` from `t` WITH CASCADED CHECK OPTION",
			reason: "CHECK OPTION",
		},
		{name: "null-safe equal",
			body:   "select (`t`.`a` <=> 1) AS `a` from `t`",
			reason: "<=>",
		},
		{name: "integer division keyword",
			body:   "select (`t`.`a` DIV 2) AS `a` from `t`",
			reason: "DIV",
		},
		{name: "window function",
			body:   "select row_number() OVER (ORDER BY `t`.`a`) AS `n` from `t`",
			reason: "ROW_NUMBER",
		},
		{name: "cast",
			body:   "select cast(`t`.`a` as char charset utf8mb4) AS `a` from `t`",
			reason: "CAST",
		},
		{name: "union",
			body:    "select `a`.`x` AS `x` from `a` union all select `b`.`x` AS `x` from `b`",
			want:    "CREATE VIEW `v` AS select `a`.`x` AS `x` from `a` union all select `b`.`x` AS `x` from `b`;\n",
			caution: []string{"UNION"}},
		{name: "unterminated string",
			body:   "select 'abc from `t`",
			reason: "unterminated",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, caution, reason := convertSQLiteView("v", viewHead+tc.body)
			if tc.reason != "" {
				if reason == "" {
					t.Fatalf("converted to %q, want a refusal containing %q", got, tc.reason)
				}
				if !strings.Contains(reason, tc.reason) {
					t.Fatalf("reason = %q, want it to contain %q", reason, tc.reason)
				}
				return
			}
			if reason != "" {
				t.Fatalf("refused: %s", reason)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if !reflect.DeepEqual(caution, tc.caution) {
				t.Errorf("caution = %q, want %q", caution, tc.caution)
			}
		})
	}
}

func TestConvertSQLiteViewHeader(t *testing.T) {
	for _, create := range []string{
		"CREATE ALGORITHM=MERGE DEFINER=`a b`@`%` SQL SECURITY INVOKER VIEW `v` AS select 1 AS `x`",
		"CREATE VIEW `v` AS select 1 AS `x`",
	} {
		got, _, reason := convertSQLiteView("v", create)
		if reason != "" {
			t.Fatalf("%s: refused: %s", create, reason)
		}
		if want := "CREATE VIEW `v` AS select 1 AS `x`;\n"; got != want {
			t.Errorf("%s: got %q, want %q", create, got, want)
		}
	}
	if _, _, reason := convertSQLiteView("v", "something else"); reason == "" {
		t.Error("a statement that is not CREATE VIEW must be refused")
	}
}

func viewSchema(views []schemafile.View, deps map[string][]string) Schema {
	return Schema{
		DB: "d",
		Tables: []source.TableInfo{{Name: "t", Columns: []sqlvalue.Column{
			{Name: "a", TypeName: "INT"}, {Name: "b", TypeName: "VARCHAR"}}}},
		Defs: map[string]source.TableDef{"t": {Columns: []source.ColumnDef{
			{Name: "a", Type: "int", DataType: "int", Nullable: true},
			{Name: "b", Type: "varchar(10)", DataType: "varchar", Nullable: true}}}},
		Views:    views,
		ViewDeps: deps,
	}
}

func writeViewSchema(t *testing.T, s Schema) (string, []string) {
	t.Helper()
	var warnings []string
	s.Warn = func(msg string) { warnings = append(warnings, msg) }
	var buf bytes.Buffer
	if err := NewSQLite(datafile.Options{}).WriteSchema(&buf, s); err != nil {
		t.Fatal(err)
	}
	return buf.String(), warnings
}

func TestSQLiteViews(t *testing.T) {
	views := []schemafile.View{
		{Name: "b_over", Create: viewHead + "select `v_a`.`a` AS `a` from `v_a`"},
		{Name: "a_bad", Create: viewHead + "select date_format(`t`.`b`,_utf8mb4'%Y') AS `y` from `t`"},
		{Name: "c_uses_bad", Create: viewHead + "select `a_bad`.`y` AS `y` from `a_bad`"},
		{Name: "d_foreign", Create: viewHead + "select `o`.`x`.`a` AS `a` from `o`.`x`", Reads: []string{"o"}},
		{Name: "v_a", Create: viewHead + "select `t`.`a` AS `a` from `t`"},
	}
	deps := map[string][]string{"b_over": {"v_a"}, "c_uses_bad": {"a_bad"}}
	got, warnings := writeViewSchema(t, viewSchema(views, deps))

	if want := "CREATE VIEW `v_a` AS select `t`.`a` AS `a` from `t`;\n" +
		"CREATE VIEW `b_over` AS select `v_a`.`a` AS `a` from `v_a`;\n"; !strings.HasSuffix(got, want) {
		t.Errorf("views are not at the end in dependency order:\n%s", got)
	}
	if strings.Contains(got, "a_bad") || strings.Contains(got, "d_foreign") || strings.Contains(got, "c_uses_bad") {
		t.Errorf("a view that was not converted is in the output:\n%s", got)
	}
	for _, want := range []string{
		"view `a_bad` is not output: DATE_FORMAT() cannot be converted to SQLite",
		"view `c_uses_bad` is not output: it reads the view `a_bad`, which is not output",
		"view `d_foreign` is not output: it reads another database (`o`)",
	} {
		if !slices.Contains(warnings, want) {
			t.Errorf("missing warning %q in %q", want, warnings)
		}
	}
	for _, w := range warnings {
		if strings.Contains(w, "v_a") || strings.Contains(w, "b_over") {
			t.Errorf("a converted view must not warn: %q", w)
		}
	}

	again, warnings2 := writeViewSchema(t, viewSchema(views, deps))
	if again != got || !reflect.DeepEqual(warnings, warnings2) {
		t.Error("the output is not deterministic")
	}
}

func TestSQLiteViewCautionWarning(t *testing.T) {
	views := []schemafile.View{{Name: "g", Create: viewHead +
		"select `t`.`b` AS `b`,count(0) AS `n` from `t` where (`t`.`b` = _utf8mb4'x') group by `t`.`b`"}}
	got, warnings := writeViewSchema(t, viewSchema(views, nil))
	if !strings.Contains(got, "CREATE VIEW `g` AS") {
		t.Fatalf("the view is not output:\n%s", got)
	}
	want := "view `g` is output, but its result may differ from MySQL: a string literal, GROUP BY " +
		"(SQLite compares and sorts text byte by byte, and divides integers as integers)"
	if !slices.Contains(warnings, want) {
		t.Errorf("missing warning %q in %q", want, warnings)
	}
}

// The converted views must be accepted by a real SQLite and return the rows.
func TestSQLiteViewsRestore(t *testing.T) {
	views := []schemafile.View{
		{Name: "v_big", Create: viewHead + "select `t`.`a` AS `a`,if((`t`.`a` > 1),_utf8mb4'big',_utf8mb4'small') AS `s`," +
			"concat(`t`.`b`,_utf8mb4'-',`t`.`b`) AS `bb` from `t` where (`t`.`a` is not null) order by `t`.`a`"},
		{Name: "v_sum", Create: viewHead + "select count(0) AS `n`,sum(`v_big`.`a`) AS `total` from `v_big`"},
	}
	got, _ := writeViewSchema(t, viewSchema(views, map[string][]string{"v_sum": {"v_big"}}))
	db := memDB(t)
	sqliteExec(t, db, got)
	sqliteExec(t, db, "INSERT INTO `t` VALUES (1,'x'),(2,'y'),(NULL,'z');")

	rows, err := db.Query("SELECT a, s, bb FROM v_big ORDER BY a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var a int
		var s, bb string
		if err := rows.Scan(&a, &s, &bb); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, strings.Join([]string{string(rune('0' + a)), s, bb}, ","))
	}
	if want := []string{"1,small,x-x", "2,big,y-y"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("rows = %q, want %q", lines, want)
	}
	var n, total int
	if err := db.QueryRow("SELECT n, total FROM v_sum").Scan(&n, &total); err != nil {
		t.Fatal(err)
	}
	if n != 2 || total != 3 {
		t.Errorf("v_sum = (%d, %d), want (2, 3)", n, total)
	}
}
