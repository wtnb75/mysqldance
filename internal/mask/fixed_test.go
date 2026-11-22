package mask

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

func col(name, typeName, columnType, dataType, charset string, nullable bool) CatalogColumn {
	return CatalogColumn{Name: name, TypeName: typeName, ColumnType: columnType, DataType: dataType, CharacterSet: charset, Nullable: nullable}
}

// builtinCatalog is a shop with the key shapes the checks look at.
func builtinCatalog() Catalog {
	return Catalog{Databases: map[string][]CatalogTable{
		"shop": {
			{
				Name: "users",
				Columns: []CatalogColumn{
					col("id", "INT", "int", "int", "", false),
					col("name", "VARCHAR", "varchar(5)", "varchar", "utf8mb4", true),
					col("short", "VARCHAR", "varchar(5)", "varchar", "utf8mb3", true),
					col("ascii", "VARCHAR", "varchar(5)", "varchar", "latin1", true),
					col("age", "TINYINT", "tinyint unsigned", "tinyint", "", true),
					col("flag", "TINYINT", "tinyint(1)", "tinyint", "", true),
					col("price", "DECIMAL", "decimal(10,2)", "decimal", "", true),
					col("ratio", "FLOAT", "float", "float", "", true),
					col("born", "DATE", "date", "date", "", true),
					col("seen", "DATETIME", "datetime(3)", "datetime", "", true),
					col("stamp", "TIMESTAMP", "timestamp", "timestamp", "", true),
					col("at", "TIME", "time(2)", "time", "", true),
					col("yr", "YEAR", "year", "year", "", true),
					col("blob", "BLOB", "blob", "blob", "", true),
					col("code", "VARCHAR", "varchar(10)", "varchar", "utf8mb4", true),
					col("ref", "INT", "int", "int", "", true),
					col("req", "INT", "int", "int", "", false),
					col("parent_id", "INT", "int", "int", "", true),
					col("stamp1", "TIMESTAMP", "timestamp(1)", "timestamp", "", true),
				},
				OrderBy: []string{"id"},
				Indexes: []CatalogIndex{
					{Name: "PRIMARY", Unique: true, Columns: []string{"id"}},
					{Name: "uq_code", Unique: true, Columns: []string{"code"}},
				},
				ForeignKeys: []CatalogFK{{Name: "fk_ref", Columns: []string{"ref"}, RefDB: "shop", RefTable: "groups", RefColumns: []string{"id"}}},
			},
			{
				Name:    "groups",
				Columns: []CatalogColumn{col("id", "INT", "int", "int", "", false)},
				OrderBy: []string{"id"},
				Indexes: []CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
			},
		},
	}}
}

func resolveRules(t *testing.T, cat Catalog, src string, salt *Salt) (*Plan, error) {
	t.Helper()
	reg := DefaultRegistry()
	r, err := Parse([]byte(src), reg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r.Resolve(cat, reg, salt)
}

func fixedRule(column, value string) string {
	return "version: 1\nrules:\n  shop.users." + column + ":\n    use: fixed\n    value: " + value + "\n"
}

func TestFixedFits(t *testing.T) {
	cases := []struct {
		column, value string
	}{
		{"name", "abcde"},
		{"name", `"日本語"`},
		{"name", `"😀"`}, // utf8mb4 holds it
		{"ascii", "abcde"},
		{"age", "255"},
		{"age", "0"},
		{"flag", "true"},
		{"price", "12345678.99"},
		{"price", "-5"},
		{"ratio", "1.5"},
		{"ratio", "3"},
		{"born", `"2020-02-29"`},
		{"born", "2020-02-29"}, // YAML reads an unquoted date as a timestamp
		{"seen", "2020-01-02 03:04:05.12"},
		{"seen", `"2020-01-02 03:04:05.12"`},
		{"stamp", `"2038-01-19 03:14:07"`},
		{"at", `"-838:59:59"`},
		{"yr", `"2155"`},
	}
	for _, c := range cases {
		t.Run(c.column+"="+c.value, func(t *testing.T) {
			if _, err := resolveRules(t, builtinCatalog(), fixedRule(c.column, c.value), nil); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
		})
	}
}

func TestFixedDoesNotFit(t *testing.T) {
	cases := []struct {
		column, value, want string
	}{
		{"name", "abcdef", "longer than 5 characters"},
		{"short", `"😀"`, "basic multilingual plane"},
		{"ascii", `"é"`, "ASCII"},
		{"name", `12`, "takes string values"},
		{"age", `"12"`, "takes integer values"},
		{"age", "256", "out of the range"},
		{"age", "-1", "out of the range"},
		{"age", "1.5", "takes integer values"},
		{"age", "true", "TINYINT(1)"},
		{"price", "true", "takes integer or decimal number values"},
		{"price", "123456789.00", "more than 10 digits"},
		{"price", "1.234", "more than 2 decimals"},
		{"price", `"1.5"`, "takes integer or decimal number values"},
		{"ratio", "1e39", "out of the range"},
		{"born", `"2021-02-29"`, "not a date"},
		{"born", `"2020-1-2"`, "YYYY-MM-DD"},
		{"seen", `"2020-01-02 03:04:05.1234"`, "more than 3 digits"},
		{"stamp", `"2038-01-19 03:14:08"`, "TIMESTAMP"},
		{"stamp", `"1970-01-01 00:00:00"`, "TIMESTAMP"},
		{"at", `"839:00:00"`, "TIME"},
		{"at", `"838:59:59.5"`, "TIME"},
		{"at", `"-838:59:59.01"`, "TIME"},
		{"stamp1", `"2038-01-19 03:14:07.5"`, "TIMESTAMP"},
		{"at", `"01:02:03.123"`, "more than 2 digits"},
		{"yr", `"1900"`, "YEAR"},
		{"yr", "2020", "takes string values"},
		{"blob", "x", "does not apply"},
	}
	for _, c := range cases {
		t.Run(c.column+"="+c.value, func(t *testing.T) {
			_, err := resolveRules(t, builtinCatalog(), fixedRule(c.column, c.value), nil)
			if err == nil {
				t.Fatal("Resolve succeeded")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// The error names the rule and the column, never the value.
func TestFixedErrorNamesNoValue(t *testing.T) {
	_, err := resolveRules(t, builtinCatalog(), fixedRule("name", "secretvalue"), nil)
	if err == nil {
		t.Fatal("Resolve succeeded")
	}
	if strings.Contains(err.Error(), "secretvalue") {
		t.Errorf("the error shows the value: %v", err)
	}
}

func TestFixedKeepsNumbersExact(t *testing.T) {
	cat := Catalog{Databases: map[string][]CatalogTable{"shop": {{
		Name:    "users",
		Columns: []CatalogColumn{col("big", "DECIMAL", "decimal(30,10)", "decimal", "", true), col("n", "BIGINT", "bigint unsigned", "bigint", "", true)},
	}}}}
	plan, err := resolveRules(t, cat,
		"version: 1\nrules:\n  shop.users.big:\n    use: fixed\n    value: 12345678901234567890.1234567891\n"+
			"  shop.users.n:\n    use: fixed\n    value: 18446744073709551615\n", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cols := []sqlvalue.Column{{Name: "big", TypeName: "DECIMAL"}, {Name: "n", TypeName: "UNSIGNED BIGINT"}}
	fn, err := plan.Table("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	row := []sqlvalue.Value{sqlvalue.NewValue("DECIMAL", []byte("1.0000000000"), false), sqlvalue.NewValue("UNSIGNED BIGINT", []byte("1"), false)}
	if err := fn(row); err != nil {
		t.Fatal(err)
	}
	if got := string(row[0].Bytes()); got != "12345678901234567890.1234567891" {
		t.Errorf("decimal = %s", got)
	}
	if got := string(row[1].Bytes()); got != "18446744073709551615" {
		t.Errorf("bigint = %s", got)
	}
}

func TestFixedWritesTheTextTheServerPrints(t *testing.T) {
	plan, err := resolveRules(t, builtinCatalog(),
		"version: 1\nrules:\n  shop.users.price:\n    use: fixed\n    value: 1.5\n"+
			"  shop.users.seen:\n    use: fixed\n    value: \"2020-01-02 03:04:05.1\"\n"+
			"  shop.users.at:\n    use: fixed\n    value: \"5:06:07\"\n"+
			"  shop.users.flag:\n    use: fixed\n    value: true\n", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cols := usersRowColumns()
	fn, err := plan.Table("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	row := usersRow(cols)
	if err := fn(row); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"price": "1.50", "seen": "2020-01-02 03:04:05.100", "at": "05:06:07.00", "flag": "1"}
	for i, c := range cols {
		if w, ok := want[c.Name]; ok && string(row[i].Bytes()) != w {
			t.Errorf("%s = %q, want %q", c.Name, row[i].Bytes(), w)
		}
	}
}

func usersRowColumns() []sqlvalue.Column {
	var cols []sqlvalue.Column
	for _, c := range builtinCatalog().Databases["shop"][0].Columns {
		cols = append(cols, sqlvalue.Column{Name: c.Name, TypeName: c.TypeName})
	}
	return cols
}

func usersRow(cols []sqlvalue.Column) []sqlvalue.Value {
	row := make([]sqlvalue.Value, len(cols))
	for i, c := range cols {
		row[i] = sqlvalue.NewValue(c.TypeName, []byte("0"), false)
	}
	return row
}

func TestFixedKeepsNull(t *testing.T) {
	plan, err := resolveRules(t, builtinCatalog(), fixedRule("name", "abc"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cols := usersRowColumns()
	fn, err := plan.Table("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	row := usersRow(cols)
	row[1] = sqlvalue.NewValue("VARCHAR", nil, true)
	if err := fn(row); err != nil {
		t.Fatal(err)
	}
	if !row[1].IsNull() {
		t.Error("NULL was replaced")
	}
}

func TestNullify(t *testing.T) {
	plan, err := resolveRules(t, builtinCatalog(), "version: 1\nrules:\n  shop.users.name:\n    use: nullify\n  shop.users.blob:\n    use: nullify\n", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cols := usersRowColumns()
	fn, err := plan.Table("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	row := usersRow(cols)
	if err := fn(row); err != nil {
		t.Fatal(err)
	}
	for i, c := range cols {
		switch c.Name {
		case "name", "blob":
			if !row[i].IsNull() || row[i].Type() != c.TypeName {
				t.Errorf("%s = %v (type %s)", c.Name, row[i].IsNull(), row[i].Type())
			}
		default:
			if row[i].IsNull() {
				t.Errorf("%s was nulled", c.Name)
			}
		}
	}
}

func TestNullifyRefusesNotNull(t *testing.T) {
	_, err := resolveRules(t, builtinCatalog(), "version: 1\nrules:\n  shop.users.req:\n    use: nullify\n", nil)
	if err == nil || !strings.Contains(err.Error(), "NOT NULL") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyColumns(t *testing.T) {
	cases := []struct {
		name, rule, want string // want "" means allowed
	}{
		{"fixed on the primary key", fixedRule("id", "1"), "part of the key the rows are ordered by"},
		{"fixed on a unique column", fixedRule("code", "x"), `unique index "uq_code"`},
		{"fixed on a foreign key child", fixedRule("ref", "1"), `foreign key "fk_ref"`},
		{"fixed on a foreign key parent", "version: 1\nrules:\n  shop.groups.id:\n    use: fixed\n    value: 1\n", "referenced by a foreign key of table shop.users"},
		{"nullify on a foreign key parent", "version: 1\nrules:\n  shop.groups.id:\n    use: nullify\n", "referenced by a foreign key of table shop.users"},
		{"nullify on a nullable unique column", "version: 1\nrules:\n  shop.users.code:\n    use: nullify\n", ""},
		{"nullify on a nullable foreign key child", "version: 1\nrules:\n  shop.users.ref:\n    use: nullify\n", ""},
		{"nullify on the primary key", "version: 1\nrules:\n  shop.users.id:\n    use: nullify\n", "NOT NULL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveRules(t, builtinCatalog(), c.rule, nil)
			if c.want == "" {
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A table that another table references is found across databases, and a
// referencing table that is left out of the dump does not count.
func TestForeignKeyParentAcrossDatabases(t *testing.T) {
	cat := builtinCatalog()
	cat.Databases["other"] = []CatalogTable{{
		Name:        "kids",
		Columns:     []CatalogColumn{col("g", "INT", "int", "int", "", true)},
		ForeignKeys: []CatalogFK{{Name: "fk", Columns: []string{"g"}, RefDB: "shop", RefTable: "users", RefColumns: []string{"name"}}},
	}}
	_, err := resolveRules(t, cat, "version: 1\nrules:\n  shop.users.name:\n    use: nullify\n", nil)
	if err == nil || !strings.Contains(err.Error(), "foreign key of table other.kids") {
		t.Fatalf("err = %v", err)
	}
	cat.Databases["other"][0].Excluded = true
	if _, err := resolveRules(t, cat, "version: 1\nrules:\n  shop.users.name:\n    use: nullify\n", nil); err != nil {
		t.Fatalf("an excluded child still blocks: %v", err)
	}
}

func TestNeedsSaltOnlyForTransformsThatUseIt(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Transform{Name: "salty", Version: 1, NeedsSalt: true, Build: func(c BuildContext) (Func, error) {
		return func(in Input) (sqlvalue.Value, error) { return in.Value, nil }, nil
	}}); err != nil {
		t.Fatal(err)
	}
	r, err := Parse([]byte("version: 1\nrules:\n  shop.users.name:\n    use: salty\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(builtinCatalog(), reg, nil); err == nil || !strings.Contains(err.Error(), "needs a salt") {
		t.Fatalf("err = %v", err)
	}
	salt, err := LoadSalt("", func(string) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(builtinCatalog(), reg, &salt); err != nil {
		t.Fatalf("Resolve with a salt: %v", err)
	}
	// fixed and nullify need none.
	if _, err := resolveRules(t, builtinCatalog(), fixedRule("name", "a"), nil); err != nil {
		t.Fatalf("fixed without a salt: %v", err)
	}
}

func TestRegisterSaltTransformNeedsVersion(t *testing.T) {
	if err := NewRegistry().Register(Transform{Name: "salty", NeedsSalt: true}); err == nil {
		t.Fatal("a transform that uses the salt registered without a version")
	}
}

func TestDeriveKey(t *testing.T) {
	salt, err := LoadSalt("", func(string) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	other, err := LoadSalt("", func(string) string { return "fedcba9876543210" })
	if err != nil {
		t.Fatal(err)
	}
	k := DeriveKey(salt, "hash", 1)
	if !bytes.Equal(k, DeriveKey(salt, "hash", 1)) {
		t.Error("the same input gives another key")
	}
	for name, got := range map[string][]byte{
		"name":    DeriveKey(salt, "other", 1),
		"version": DeriveKey(salt, "hash", 2),
		"salt":    DeriveKey(other, "hash", 1),
	} {
		if bytes.Equal(k, got) {
			t.Errorf("a different %s gives the same key", name)
		}
	}
	// "ab"+"c" and "a"+"bc" must not meet.
	if bytes.Equal(DeriveKey(salt, "ab", 1), DeriveKey(salt, "a", 11)) {
		t.Error("name and version run together")
	}
	if len(k) != 32 {
		t.Errorf("key is %d bytes", len(k))
	}
}

// With several rules on a table, each transform sees the row as it was read.
func TestPlanTransformsSeeTheRowAsRead(t *testing.T) {
	reg := NewRegistry()
	var seen []string
	if err := reg.Register(Transform{Name: "peek", Build: func(c BuildContext) (Func, error) {
		return func(in Input) (sqlvalue.Value, error) {
			seen = append(seen, string(in.Row[0].Bytes()))
			return sqlvalue.NewValue("INT", []byte("9"), false), nil
		}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	cat := Catalog{Databases: map[string][]CatalogTable{"d": {{Name: "t", Columns: []CatalogColumn{
		{Name: "a", TypeName: "INT"}, {Name: "b", TypeName: "INT"}}}}}}
	r, err := Parse([]byte("version: 1\nrules:\n  d.t.a:\n    use: peek\n  d.t.b:\n    use: peek\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.Resolve(cat, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := plan.Table("d", "t", []sqlvalue.Column{{Name: "a", TypeName: "INT"}, {Name: "b", TypeName: "INT"}})
	if err != nil {
		t.Fatal(err)
	}
	row := []sqlvalue.Value{sqlvalue.NewValue("INT", []byte("1"), false), sqlvalue.NewValue("INT", []byte("2"), false)}
	if err := fn(row); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "1" || seen[1] != "1" {
		t.Errorf("transforms saw %v, want the original row twice", seen)
	}
}

func TestPlanTableWithoutRulesIsNil(t *testing.T) {
	plan, err := resolveRules(t, builtinCatalog(), fixedRule("name", "a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := plan.Table("shop", "groups", []sqlvalue.Column{{Name: "id", TypeName: "INT"}})
	if err != nil || fn != nil {
		t.Fatalf("fn = %v, err = %v; a table without rules needs no function", fn != nil, err)
	}
}

func TestPlanRefusesMovedColumns(t *testing.T) {
	plan, err := resolveRules(t, builtinCatalog(), fixedRule("name", "a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cols := usersRowColumns()
	cols[1], cols[2] = cols[2], cols[1]
	if _, err := plan.Table("shop", "users", cols); err == nil {
		t.Fatal("a rule was applied to another column")
	}
}
