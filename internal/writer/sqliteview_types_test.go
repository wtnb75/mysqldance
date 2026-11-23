package writer

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

const (
	numText = "a numeric column held as TEXT (compared or calculated as text)"
	ciText  = "a case-insensitive text column (SQLite compares it case-sensitively)"
)

func typeDefs() map[string]source.TableDef {
	col := func(name, typ, data, collation string) source.ColumnDef {
		return source.ColumnDef{Name: name, Type: typ, DataType: data, Collation: collation, Nullable: true}
	}
	return map[string]source.TableDef{
		"t": {Columns: []source.ColumnDef{
			col("id", "int", "int", ""),
			col("n", "int", "int", ""),
			col("price", "decimal(10,2)", "decimal", ""),
			col("big", "bigint unsigned", "bigint", ""),
			col("bits64", "bit(64)", "bit", ""),
			col("bits8", "bit(8)", "bit", ""),
			col("name", "varchar(10)", "varchar", "utf8mb4_0900_ai_ci"),
			col("code", "varchar(10)", "varchar", "utf8mb4_bin"),
			col("kind", "enum('a','b')", "enum", "utf8mb4_general_ci"),
		}},
		"u": {Columns: []source.ColumnDef{
			col("id", "int", "int", ""),
			col("tax", "decimal(10,2)", "decimal", ""),
			col("name", "varchar(10)", "varchar", "utf8mb4_general_ci"),
			col("code", "varchar(10)", "varchar", "utf8mb4_bin"),
		}},
	}
}

func TestSQLiteViewTypeCautions(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		caution []string
	}{
		{name: "decimal compared with an integer",
			body:    "select `t`.`id` AS `id` from `t` where (`t`.`price` > 5)",
			caution: []string{numText}},
		{name: "integer on the left of the operator",
			body:    "select `t`.`id` AS `id` from `t` where (5 <= `t`.`price`)",
			caution: []string{numText}},
		{name: "bigint unsigned",
			body:    "select `t`.`id` AS `id` from `t` where (`t`.`big` = 5)",
			caution: []string{numText}},
		{name: "bit 64",
			body:    "select `t`.`id` AS `id` from `t` where (`t`.`bits64` <> 5)",
			caution: []string{numText}},
		{name: "bit 8 is an integer",
			body: "select `t`.`id` AS `id` from `t` where (`t`.`bits8` > 5)"},
		{name: "int is an integer",
			body: "select `t`.`id` AS `id` from `t` where (`t`.`n` > 5)"},
		{name: "arithmetic of two decimals",
			body:    "select (`t`.`price` + `u`.`tax`) AS `x` from (`t` join `u` on((`t`.`id` = `u`.`id`)))",
			caution: []string{numText}},
		{name: "multiplication",
			body:    "select (`t`.`n` * `t`.`price`) AS `x` from `t`",
			caution: []string{numText}},
		{name: "a decimal that is only selected",
			body: "select `t`.`price` AS `price` from `t`"},
		{name: "a decimal in ifnull",
			body: "select ifnull(`t`.`price`,0) AS `price` from `t`"},
		{name: "a decimal in count",
			body: "select count(`t`.`price`) AS `n` from `t`"},
		{name: "sum of a decimal",
			body:    "select sum(`t`.`price`) AS `s` from `t`",
			caution: []string{"SUM()", numText}},
		{name: "avg, min, max, round and abs of a decimal",
			body: "select avg(`t`.`price`) AS `a`,min(`t`.`price`) AS `b`,max(`t`.`price`) AS `c`," +
				"round(`t`.`price`,2) AS `d`,abs(`t`.`price`) AS `e` from `t`",
			caution: []string{"AVG()", "ROUND()", numText}},
		{name: "decimal as the subject of between",
			body:    "select `t`.`id` AS `id` from `t` where (`t`.`price` between 1 and 5)",
			caution: []string{numText}},
		{name: "decimal as the lower bound of between",
			body:    "select `t`.`id` AS `id` from `t` where (`t`.`n` between `t`.`price` and 9)",
			caution: []string{numText}},
		{name: "decimal as the upper bound of between",
			body:    "select `t`.`id` AS `id` from `t` where (`t`.`n` between 1 and `t`.`price`)",
			caution: []string{numText}},
		{name: "a decimal after and that is not a bound of between",
			body: "select `t`.`id` AS `id` from `t` where ((`t`.`n` between 1 and 2) and `t`.`price`)"},
		{name: "table alias",
			body: "select `a`.`id` AS `id` from (`t` `a` join `u` `b` on((`a`.`id` = `b`.`id`))) where (`b`.`tax` > `a`.`n`)",
			// tax is decimal, reached through the alias b
			caution: []string{numText}},
		{name: "case-insensitive columns joined",
			body:    "select `t`.`id` AS `id` from (`t` join `u` on((`t`.`name` = `u`.`name`)))",
			caution: []string{ciText}},
		{name: "case-insensitive columns compared with less than",
			body:    "select `t`.`id` AS `id` from (`t` join `u`) where (`t`.`kind` < `u`.`name`)",
			caution: []string{ciText}},
		{name: "case-insensitive column against a binary one",
			body:    "select `t`.`id` AS `id` from (`t` join `u`) where (`t`.`name` = `u`.`code`)",
			caution: []string{ciText}},
		{name: "binary columns compared",
			body: "select `t`.`id` AS `id` from (`t` join `u` on((`t`.`code` = `u`.`code`)))"},
		{name: "case-insensitive column against a literal is the literal's warning",
			body:    "select `t`.`id` AS `id` from `t` where (`t`.`name` = _utf8mb4'x')",
			caution: []string{"a string literal"}},
		{name: "max of a case-insensitive column",
			body:    "select max(`t`.`name`) AS `m` from `t`",
			caution: []string{ciText}},
		{name: "min of a binary column",
			body: "select min(`t`.`code`) AS `m` from `t`"},
		{name: "min of an integer",
			body: "select min(`t`.`n`) AS `m` from `t`"},
		{name: "a derived table's column is not resolved",
			body: "select `x`.`price` AS `price` from (select `t`.`price` AS `price` from `t`) `x` where (`x`.`price` > 5)"},
		{name: "a view's column is not resolved",
			body: "select `v1`.`price` AS `price` from `v1` where (`v1`.`price` > 5)"},
		{name: "an unknown column is not resolved",
			body: "select `t`.`id` AS `id` from `t` where (`t`.`nothing` > 5)"},
		{name: "an unknown table is not resolved",
			body: "select `z`.`id` AS `id` from `z` where (`z`.`price` > 5)"},
		{name: "another database's table is not resolved",
			body: "select `t`.`id` AS `id` from `t` where (`o`.`t`.`price` > 5)"},
		{name: "a derived table with the alias of a base table is not resolved",
			body: "select `t`.`id` AS `id` from `t` where `t`.`id` in (select `t`.`price` from " +
				"(select `u`.`id` AS `price` from `u`) `t` where (`t`.`price` > 5))"},
		// lower_case_table_names=2 keeps the case of the definition, which can
		// differ from the name the catalog lists the table by.
		{name: "a table name that differs only in case is resolved",
			body:    "select `T`.`id` AS `id` from `T` where (`T`.`price` > 5)",
			caution: []string{numText}},
		{name: "an alias that follows a qualified table is not resolved",
			body: "select `x`.`id` AS `id` from `o`.`t` `x` where `x`.`id` in " +
				"(select `x`.`id` from `t` `x` where (`x`.`price` > 5))"},
		{name: "the same alias for two tables is not resolved",
			body: "select `a`.`id` AS `id` from `t` `a` where `a`.`id` in (select `a`.`id` from `u` `a` where (`a`.`tax` > 5))"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, caution, reason := convertView(sqliteDialect, nil, typeDefs(), "v", viewHead+tc.body)
			if reason != "" {
				t.Fatalf("refused: %s", reason)
			}
			if !reflect.DeepEqual(caution, tc.caution) {
				t.Errorf("caution = %q, want %q", caution, tc.caution)
			}
		})
	}
}

// Without the definitions the conversion is the syntax-only one of B-062.
func TestSQLiteViewTypeCautionsNeedDefs(t *testing.T) {
	_, caution, reason := convertView(sqliteDialect, nil, nil, "v",
		viewHead+"select `t`.`id` AS `id` from `t` where (`t`.`price` > 5)")
	if reason != "" || len(caution) != 0 {
		t.Errorf("caution = %q, reason = %q; want none", caution, reason)
	}
}

func TestSQLiteViewTypeWarning(t *testing.T) {
	views := []schemafile.View{
		{Name: "v_dec", Create: viewHead + "select `t`.`id` AS `id` from `t` where (`t`.`price` > 5)"},
		{Name: "v_int", Create: viewHead + "select `t`.`id` AS `id` from `t` where (`t`.`n` > 5)"},
	}
	s := viewSchema(views, nil)
	s.Tables = []source.TableInfo{{Name: "t", Columns: []sqlvalue.Column{
		{Name: "id", TypeName: "INT"}, {Name: "n", TypeName: "INT"}, {Name: "price", TypeName: "DECIMAL"}}}}
	s.Defs = map[string]source.TableDef{"t": {Columns: typeDefs()["t"].Columns[:3]}}
	got, warnings := writeViewSchema(t, s)
	if !strings.Contains(got, "CREATE VIEW `v_dec` AS") || !strings.Contains(got, "CREATE VIEW `v_int` AS") {
		t.Fatalf("a view is not output:\n%s", got)
	}
	want := "view `v_dec` is output, but its result may differ from MySQL: " + numText +
		" (SQLite compares and sorts text byte by byte, and divides integers as integers)"
	if !slices.Contains(warnings, want) {
		t.Errorf("missing warning %q in %q", want, warnings)
	}
	for _, w := range warnings {
		if strings.Contains(w, "v_int") {
			t.Errorf("an integer view must not warn: %q", w)
		}
	}
	again, warnings2 := writeViewSchema(t, s)
	if again != got || !reflect.DeepEqual(warnings, warnings2) {
		t.Error("the output is not deterministic")
	}
}
