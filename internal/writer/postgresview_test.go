package writer

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
)

func TestPostgresViews(t *testing.T) {
	views := []schemafile.View{
		{Name: "b_over", Create: viewHead + "select `v_a`.`a` AS `a` from `v_a`"},
		{Name: "a_bad", Create: viewHead + "select date_format(`child`.`created`,_utf8mb4'%Y') AS `y` from `child`"},
		{Name: "c_uses_bad", Create: viewHead + "select `a_bad`.`y` AS `y` from `a_bad`"},
		{Name: "d_foreign", Create: viewHead + "select `o`.`x`.`a` AS `a` from `o`.`x`", Reads: []string{"o"}},
		{Name: "e_flag", Create: viewHead + "select `child`.`id` AS `id` from `child` where (`child`.`note` = 1)"},
		{Name: "v_a", Create: viewHead + "select `child`.`id` AS `a` from `child` order by `child`.`id`"},
		{Name: "parent__uk_code", Create: viewHead + "select 1 AS `a`"},
	}
	deps := map[string][]string{"b_over": {"v_a"}, "c_uses_bad": {"a_bad"}}
	s := relationalSchema()
	s.Views = views
	s.ViewDeps = deps
	s.Defs["child"] = withBoolColumn(s.Defs["child"])

	got, warnings := writeSchemaText(t, s)
	if want := "CREATE VIEW \"v_a\" AS select \"child\".\"id\" AS \"a\" from \"child\" order by \"child\".\"id\";\n" +
		"CREATE VIEW \"b_over\" AS select \"v_a\".\"a\" AS \"a\" from \"v_a\";\n"; !strings.HasSuffix(got, want) {
		t.Errorf("views are not at the end in dependency order:\n%s", got)
	}
	for _, name := range []string{"a_bad", "c_uses_bad", "d_foreign", "e_flag", "parent__uk_code"} {
		if strings.Contains(got, "VIEW \""+name+"\"") {
			t.Errorf("view %s was not converted but is in the output:\n%s", name, got)
		}
	}
	for _, want := range []string{
		"view `a_bad` is not output: DATE_FORMAT() cannot be converted to PostgreSQL",
		"view `c_uses_bad` is not output: it reads the view `a_bad`, which is not output",
		"view `d_foreign` is not output: it reads another database (`o`)",
		"view `parent__uk_code` is not output: its name is already used by",
		"view `e_flag` is not output: the boolean column `note`",
		"view `v_a` is output, but its result may differ from MySQL: ORDER BY",
	} {
		if !containsAny(warnings, want) {
			t.Errorf("missing warning %q in %q", want, warnings)
		}
	}
	// The output and the warnings are the same on every run.
	again, warnings2 := writeSchemaText(t, s)
	if again != got || !reflect.DeepEqual(warnings, warnings2) {
		t.Error("two runs differ")
	}
}

// withBoolColumn makes the column note of the table a TINYINT(1).
func withBoolColumn(d source.TableDef) source.TableDef {
	d.Columns = slices.Clone(d.Columns)
	for i := range d.Columns {
		if d.Columns[i].Name == "note" {
			d.Columns[i].Type, d.Columns[i].DataType = "tinyint(1)", "tinyint"
		}
	}
	return d
}

func TestConvertPostgresView(t *testing.T) {
	env := pgViewEnv{
		BoolCols: map[string]bool{"flag": true},
		PKs:      map[string][]string{"t": {"id"}, "two": {"a", "b"}},
		NotOut:   map[string]bool{"gen": true},
	}
	tests := []struct {
		name    string
		body    string
		want    string // the converted statement; "" when the view is not converted
		caution []string
		reason  string // a substring of the reason the view is not converted
	}{
		{name: "plain select requotes identifiers",
			body: "select `t`.`a` AS `a`,count(0) AS `n` from `t` where (`t`.`a` > 1)",
			want: "CREATE VIEW \"v\" AS select \"t\".\"a\" AS \"a\",count(0) AS \"n\" from \"t\" where (\"t\".\"a\" > 1);\n"},
		{name: "join",
			body: "select `a`.`id` AS `id` from (`a` join `b` on((`a`.`id` = `b`.`id`))) left join `c` on((`c`.`id` = `a`.`id`))",
			want: "CREATE VIEW \"v\" AS select \"a\".\"id\" AS \"id\" from (\"a\" join \"b\" on((\"a\".\"id\" = \"b\".\"id\"))) left join \"c\" on((\"c\".\"id\" = \"a\".\"id\"))" + ";\n"},
		{name: "quotes inside identifiers",
			body: "select `t`.`a``b` AS `x\"y` from `t`",
			want: "CREATE VIEW \"v\" AS select \"t\".\"a`b\" AS \"x\"\"y\" from \"t\";\n"},
		{name: "ifnull becomes coalesce",
			body: "select ifnull(`t`.`a`,0) AS `x` from `t`",
			want: "CREATE VIEW \"v\" AS select coalesce(\"t\".\"a\",0) AS \"x\" from \"t\";\n"},
		{name: "if becomes case",
			body:    "select if((`t`.`a` > 1),_utf8mb4'big',_utf8mb4'small') AS `s` from `t`",
			want:    "CREATE VIEW \"v\" AS select CASE WHEN (\"t\".\"a\" > 1) THEN 'big' ELSE 'small' END AS \"s\" from \"t\";\n",
			caution: []string{"a string literal"}},
		{name: "concat casts what is not a string literal",
			body:    "select concat(`t`.`a`,_utf8mb4' it\\'s') AS `c` from `t`",
			want:    "CREATE VIEW \"v\" AS select (CAST(\"t\".\"a\" AS text) || ' it''s') AS \"c\" from \"t\";\n",
			caution: []string{"a string literal"}},
		{name: "limit with an offset",
			body:    "select `t`.`a` AS `a` from `t` order by `t`.`a` limit 5,10",
			want:    "CREATE VIEW \"v\" AS select \"t\".\"a\" AS \"a\" from \"t\" order by \"t\".\"a\" limit 10 offset 5;\n",
			caution: []string{"ORDER BY"}},
		{name: "limit alone",
			body: "select `t`.`a` AS `a` from `t` limit 3",
			want: "CREATE VIEW \"v\" AS select \"t\".\"a\" AS \"a\" from \"t\" limit 3;\n"},
		{name: "division is cautioned",
			body:    "select (`t`.`a` / 2) AS `h` from `t`",
			want:    "CREATE VIEW \"v\" AS select (\"t\".\"a\" / 2) AS \"h\" from \"t\";\n",
			caution: []string{"a division"}},
		{name: "round has no double precision form",
			body:   "select round(`t`.`a`,2) AS `r` from `t`",
			reason: "ROUND()"},
		{name: "unknown function",
			body:   "select date_format(`t`.`d`,_utf8mb4'%Y') AS `y` from `t`",
			reason: "DATE_FORMAT"},
		{name: "check option",
			body:   "select `t`.`a` AS `a` from `t` WITH CASCADED CHECK OPTION",
			reason: "CHECK OPTION"},
		{name: "a string with a NUL byte",
			body:   "select 'a\\0b' AS `a` from `t`",
			reason: "NUL"},
		{name: "a name over the identifier limit",
			body:   "select `t`.`" + strings.Repeat("a", 64) + "` AS `a` from `t`",
			reason: "63"},
		{name: "boolean column compared with a number",
			body:   "select `t`.`id` AS `id` from `t` where (`t`.`flag` = 1)",
			reason: "boolean"},
		{name: "number compared with a boolean column",
			body:   "select `t`.`id` AS `id` from `t` where (0 <> `t`.`flag`)",
			reason: "boolean"},
		{name: "boolean column in an aggregate",
			body:   "select sum(`t`.`flag`) AS `n` from `t`",
			reason: "boolean"},
		{name: "boolean column in arithmetic",
			body:   "select (`t`.`flag` + 1) AS `n` from `t`",
			reason: "boolean"},
		{name: "boolean column used as a condition and a value",
			body: "select `t`.`flag` AS `flag` from `t` where (`t`.`flag` and (`t`.`id` > 1))",
			want: "CREATE VIEW \"v\" AS select \"t\".\"flag\" AS \"flag\" from \"t\" where (\"t\".\"flag\" and (\"t\".\"id\" > 1));\n"},
		{name: "column that is not grouped",
			body:   "select `t`.`g` AS `g`,`t`.`x` AS `x` from `t` group by `t`.`g`",
			reason: "`t`.`x`"},
		{name: "grouped column and an aggregate",
			body:    "select `t`.`g` AS `g`,count(0) AS `n` from `t` group by `t`.`g`",
			want:    "CREATE VIEW \"v\" AS select \"t\".\"g\" AS \"g\",count(0) AS \"n\" from \"t\" group by \"t\".\"g\";\n",
			caution: []string{"GROUP BY"}},
		{name: "column that depends on a grouped primary key",
			body:    "select `t`.`id` AS `id`,`t`.`x` AS `x` from `t` group by `t`.`id`",
			want:    "CREATE VIEW \"v\" AS select \"t\".\"id\" AS \"id\",\"t\".\"x\" AS \"x\" from \"t\" group by \"t\".\"id\";\n",
			caution: []string{"GROUP BY"}},
		{name: "composite primary key grouped in part only",
			body:   "select `two`.`a` AS `a`,`two`.`x` AS `x` from `two` group by `two`.`a`",
			reason: "`two`.`x`"},
		{name: "a column that is not output",
			body:   "select `t`.`gen` AS `gen` from `t`",
			reason: "the column `gen`"},
		{name: "boolean column not in a list of numbers",
			body:   "select `t`.`id` AS `id` from `t` where (`t`.`flag` not in (0,1))",
			reason: "boolean"},
		{name: "limit count over what PostgreSQL holds",
			body:   "select `t`.`a` AS `a` from `t` limit 5,18446744073709551615",
			reason: "LIMIT"},
		{name: "a constant needs no grouping",
			body:    "select _utf8mb4'x' AS `kind`,`t`.`g` AS `g` from `t` group by `t`.`g`",
			want:    "CREATE VIEW \"v\" AS select 'x' AS \"kind\",\"t\".\"g\" AS \"g\" from \"t\" group by \"t\".\"g\";\n",
			caution: []string{"a string literal", "GROUP BY"}},
		{name: "boolean column in concat",
			body:   "select concat(`t`.`flag`,_utf8mb4'x') AS `c` from `t`",
			reason: "CONCAT()"},
		{name: "boolean column in coalesce",
			body:   "select coalesce(`t`.`flag`,0) AS `c` from `t`",
			reason: "COALESCE()"},
		{name: "boolean column in a list of numbers",
			body:   "select `t`.`id` AS `id` from `t` where (`t`.`flag` in (0,1))",
			reason: "boolean"},
		{name: "an aliased table gets no primary key exemption",
			body:   "select `t`.`id` AS `id`,`t`.`x` AS `x` from `u` `t` group by `t`.`id`",
			reason: "`t`.`x`"},
		{name: "a subquery in the select list is not checked",
			body:    "select `t`.`g` AS `g`,(select `u`.`a` from `u` limit 1) AS `m` from `t` group by `t`.`g`",
			want:    "CREATE VIEW \"v\" AS select \"t\".\"g\" AS \"g\",(select \"u\".\"a\" from \"u\" limit 1) AS \"m\" from \"t\" group by \"t\".\"g\";\n",
			caution: []string{"GROUP BY"}},
		{name: "union branches are checked one by one",
			body:   "select `t`.`g` AS `g` from `t` union select `t`.`g` AS `g`,`t`.`x` AS `x` from `t` group by `t`.`g`",
			reason: "`t`.`x`"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, caution, reason := convertPostgresView("v", viewHead+tc.body, env)
			if tc.reason != "" {
				if reason == "" {
					t.Fatalf("converted to %q, want a refusal containing %q", got, tc.reason)
				}
				if !strings.Contains(reason, tc.reason) {
					t.Fatalf("reason = %q, want it to contain %q", reason, tc.reason)
				}
				if got != "" {
					t.Errorf("statement = %q with a refusal, want none", got)
				}
				return
			}
			if reason != "" {
				t.Fatalf("refused: %s", reason)
			}
			if got != tc.want {
				t.Errorf("statement =\n%s\nwant\n%s", got, tc.want)
			}
			if !reflect.DeepEqual(caution, tc.caution) {
				t.Errorf("caution = %q, want %q", caution, tc.caution)
			}
		})
	}
}

func TestNewPGViewEnv(t *testing.T) {
	defs := map[string]source.TableDef{
		"t": {
			Columns: []source.ColumnDef{
				{Name: "id", Type: "int", DataType: "int"},
				{Name: "flag", Type: "tinyint(1)", DataType: "tinyint"},
				{Name: "small", Type: "tinyint(1) unsigned", DataType: "tinyint"},
				{Name: "g", Type: "int", DataType: "int", Generated: true},
			},
			Indexes: []source.IndexDef{
				{Name: "PRIMARY", Parts: []source.IndexPart{{Column: "id"}}},
				{Name: "u", Unique: true, Parts: []source.IndexPart{{Column: "flag"}}},
			},
		},
	}
	env := newPGViewEnv(defs)
	if !reflect.DeepEqual(env.BoolCols, map[string]bool{"flag": true}) {
		t.Errorf("BoolCols = %v, want only flag", env.BoolCols)
	}
	if !reflect.DeepEqual(env.NotOut, map[string]bool{"g": true}) {
		t.Errorf("NotOut = %v, want only g", env.NotOut)
	}
	if !reflect.DeepEqual(env.PKs, map[string][]string{"t": {"id"}}) {
		t.Errorf("PKs = %v, want t: id", env.PKs)
	}
}
