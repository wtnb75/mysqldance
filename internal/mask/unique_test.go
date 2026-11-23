package mask

import (
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

// uniqueCatalog has a table with a primary key, a unique index on one column, a
// composite unique index, a prefix unique index, a unique index with an
// expression, one with a generated column, and a foreign key.
func uniqueCatalog() Catalog {
	ci := func(c CatalogColumn) CatalogColumn { c.Collation = "utf8mb4_0900_ai_ci"; return c }
	bin := func(c CatalogColumn) CatalogColumn { c.Collation = "utf8mb4_bin"; return c }
	gen := col("g", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true)
	gen.Generated = true
	return Catalog{Databases: map[string][]CatalogTable{"shop": {{
		Name: "users",
		Columns: []CatalogColumn{
			col("id", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", false),
			ci(col("email", "VARCHAR", "varchar(60)", "varchar", "utf8mb4", true)),
			bin(col("code", "VARCHAR", "varchar(10)", "varchar", "utf8mb4", true)),
			ci(col("a", "VARCHAR", "varchar(10)", "varchar", "utf8mb4", true)),
			col("n", "INT", "int", "int", "", true),
			ci(col("long", "VARCHAR", "varchar(200)", "varchar", "utf8mb4", true)),
			ci(col("expr", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true)),
			ci(col("withgen", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true)),
			gen,
			ci(col("child", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true)),
			ci(col("free", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true)),
			ci(col("mixed", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true)),
			ci(col("note", "VARCHAR", "varchar(200)", "varchar", "utf8mb4", true)),
		},
		OrderBy: []string{"id"},
		Indexes: []CatalogIndex{
			{Name: "PRIMARY", Unique: true, Columns: []string{"id"}},
			{Name: "uq_email", Unique: true, Columns: []string{"email"}},
			{Name: "uq_code", Unique: true, Columns: []string{"code"}},
			{Name: "uq_pair", Unique: true, Columns: []string{"a", "n"}},
			{Name: "uq_long", Unique: true, Columns: []string{"long"}, PrefixColumns: []string{"long"}},
			{Name: "uq_expr", Unique: true, Columns: []string{"expr"}, Functional: true},
			{Name: "uq_gen", Unique: true, Columns: []string{"withgen", "g"}},
			{Name: "uq_mix", Unique: true, Columns: []string{"mixed", "note"}, PrefixColumns: []string{"note"}},
			{Name: "ix_free", Columns: []string{"free"}},
		},
		ForeignKeys: []CatalogFK{{Name: "fk_child", Columns: []string{"child"}, RefDB: "shop", RefTable: "groups", RefColumns: []string{"id"}}},
	}, {
		Name:    "groups",
		Columns: []CatalogColumn{col("id", "INT", "int", "int", "", false)},
		OrderBy: []string{"id"},
		Indexes: []CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}}}}
}

// hash_keep_shape may be used on the columns of a unique index that is not the
// primary key, because the dump checks that no two rows collide. The columns the
// dump cannot check (a prefix, an expression, a generated column) and the keys
// that stay refused (the ordering key, the primary key, a foreign key) are
// refused before the dump.
func TestHashKeepShapeOnUniqueColumns(t *testing.T) {
	salt := shapeSaltOf(t, shapeSalt)
	cases := []struct {
		column  string
		wantErr string // "" for allowed
	}{
		{"email", ""},
		{"code", ""},
		{"a", ""}, // part of a composite unique index
		{"free", ""},
		{"id", "key"},
		{"child", "foreign key"},
		{"long", "prefix"},
		{"mixed", "prefix"}, // another column of its index is indexed by a prefix
		{"expr", "expression"},
		{"withgen", "generated"},
	}
	for _, c := range cases {
		_, err := resolveRules(t, uniqueCatalog(), shapeRules(c.column), salt)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: %v", c.column, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want %q", c.column, err, c.wantErr)
		}
	}
}

// Another transform that breaks keys stays refused on a unique column.
func TestFixedStaysRefusedOnUniqueColumns(t *testing.T) {
	_, err := resolveRules(t, uniqueCatalog(), "version: 1\nrules:\n  shop.users.email:\n    use: fixed\n    value: x\n", nil)
	if err == nil || !strings.Contains(err.Error(), `unique index "uq_email"`) {
		t.Fatalf("err = %v", err)
	}
}

func uniquesOf(t *testing.T, columns ...string) []UniqueCheck {
	t.Helper()
	plan, err := resolveRules(t, uniqueCatalog(), shapeRules(columns...), shapeSaltOf(t, shapeSalt))
	if err != nil {
		t.Fatal(err)
	}
	var cols []sqlvalue.Column
	for _, c := range uniqueCatalog().Databases["shop"][0].Columns {
		if !c.Generated {
			cols = append(cols, sqlvalue.Column{Name: c.Name, TypeName: c.TypeName})
		}
	}
	checks, err := plan.Uniques("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	return checks
}

// The plan lists the unique indexes that have a transformed column: the column
// positions in the row, and whether the collation folds ASCII case (a name with
// _ci), per column. An index without a transformed column is not listed.
func TestPlanUniques(t *testing.T) {
	got := uniquesOf(t, "email", "code", "a")
	want := []UniqueCheck{
		{Index: "uq_code", Columns: []string{"code"}, Positions: []int{2}, Fold: []bool{false}},
		{Index: "uq_email", Columns: []string{"email"}, Positions: []int{1}, Fold: []bool{true}},
		{Index: "uq_pair", Columns: []string{"a", "n"}, Positions: []int{3, 4}, Fold: []bool{true, false}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("checks = %+v\nwant     %+v", got, want)
	}
	if got := uniquesOf(t, "free"); len(got) != 0 {
		t.Errorf("a column of no unique index gave checks: %+v", got)
	}
}

func TestPlanUniquesNeedsTheColumnsOfTheRules(t *testing.T) {
	plan, err := resolveRules(t, uniqueCatalog(), shapeRules("email"), shapeSaltOf(t, shapeSalt))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Uniques("shop", "users", []sqlvalue.Column{{Name: "id"}, {Name: "other"}}); err == nil {
		t.Error("a table whose columns changed was accepted")
	}
	if checks, err := plan.Uniques("shop", "groups", nil); err != nil || checks != nil {
		t.Errorf("a table without rules: %v, %v", checks, err)
	}
}
