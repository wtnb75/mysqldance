package mask

import (
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

// strictCatalog has two dumped databases, a view, an excluded table and a
// generated column, none of which may count as "undecided".
func strictCatalog() Catalog {
	return Catalog{
		Databases: map[string][]CatalogTable{
			"shop": {
				{Name: "users", Columns: []CatalogColumn{
					{Name: "id", TypeName: "INT"},
					{Name: "email", TypeName: "VARCHAR"},
					{Name: "note", TypeName: "TEXT", Nullable: true},
					{Name: "email_lower", TypeName: "VARCHAR", Generated: true},
				}},
				{Name: "orders", Columns: []CatalogColumn{
					{Name: "id", TypeName: "INT"},
					{Name: "total", TypeName: "INT"},
				}},
				{Name: "v_users", View: true, Columns: []CatalogColumn{{Name: "email", TypeName: "VARCHAR"}}},
				{Name: "audit", Excluded: true, Columns: []CatalogColumn{{Name: "msg", TypeName: "TEXT"}}},
			},
			"blog": {
				{Name: "posts", Columns: []CatalogColumn{{Name: "id", TypeName: "INT"}, {Name: "body", TypeName: "TEXT"}}},
			},
		},
	}
}

func strictRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := testRegistry(t)
	if err := reg.Register(keepTransform); err != nil {
		t.Fatal(err)
	}
	return reg
}

func strictParse(t *testing.T, src string) *Rules {
	t.Helper()
	r, err := Parse([]byte(src), strictRegistry(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func resolveStrict(t *testing.T, cat Catalog, src string) (*Rules, error) {
	t.Helper()
	r := strictParse(t, src)
	return r, r.Validate(cat, strictRegistry(t))
}

func TestKeepIsRegisteredAndLeavesValues(t *testing.T) {
	reg := DefaultRegistry()
	tr, ok := reg.Lookup("keep")
	if !ok {
		t.Fatal("keep is not registered")
	}
	if len(tr.Args) != 0 || len(tr.Types) != 0 || tr.NeedsSalt {
		t.Errorf("keep = %+v: want no arguments, every type, no salt", tr)
	}
	cat := Catalog{Databases: map[string][]CatalogTable{"d": {{Name: "t", Columns: []CatalogColumn{
		{Name: "a", TypeName: "VARCHAR", DataType: "varchar", ColumnType: "varchar(10)"},
		{Name: "b", TypeName: "JSON", DataType: "json", ColumnType: "json"},
	}}}}}
	r, err := Parse([]byte("version: 1\nrules:\n  d.t.a:\n    use: keep\n  d.t.b:\n    use: keep\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.Resolve(cat, reg, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cols := []sqlvalue.Column{{Name: "a"}, {Name: "b"}}
	fn, err := plan.Table("d", "t", cols)
	if err != nil {
		t.Fatal(err)
	}
	if fn != nil {
		t.Error("keep costs a per-row call; want none")
	}
}

// A NoChange transform is dropped from the plan without being built, so one that
// has arguments, needs a salt or has a Build would silently do nothing.
func TestRegisterRefusesNoChangeWithWork(t *testing.T) {
	for name, tr := range map[string]Transform{
		"args":  {Name: "a", NoChange: true, Args: []ArgDef{{Name: "x", Kind: ArgInt}}},
		"salt":  {Name: "b", NoChange: true, NeedsSalt: true, Version: 1},
		"build": {Name: "c", NoChange: true, Build: buildNullify},
	} {
		if err := NewRegistry().Register(tr); err == nil {
			t.Errorf("%s: Register accepted a NoChange transform that has work to do", name)
		}
	}
}

func TestKeepOnKeyColumnsIsAllowed(t *testing.T) {
	cat := Catalog{Databases: map[string][]CatalogTable{"d": {
		{Name: "p", Columns: []CatalogColumn{{Name: "id", TypeName: "INT"}}, OrderBy: []string{"id"},
			Indexes: []CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}}},
		{Name: "c", Columns: []CatalogColumn{{Name: "pid", TypeName: "INT"}},
			ForeignKeys: []CatalogFK{{Name: "fk", Columns: []string{"pid"}, RefDB: "d", RefTable: "p", RefColumns: []string{"id"}}}},
	}}}
	// keep changes nothing, so it is no transform of a key column.
	_, err := resolveStrict(t, cat, "version: 1\nrules:\n  d.p.id:\n    use: keep\n  d.c.pid:\n    use: keep\n")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestStrictColumnsListsUndecided(t *testing.T) {
	r, err := resolveStrict(t, strictCatalog(), `
version: 1
rules:
  shop.users.id:
    use: keep
  shop.users.email:
    use: fixed
    value: x
`)
	if err != nil {
		t.Fatal(err)
	}
	err = r.CheckStrictColumns(strictCatalog())
	if err == nil {
		t.Fatal("want error")
	}
	want := []string{"blog.posts.body", "blog.posts.id", "shop.orders.id", "shop.orders.total", "shop.users.note"}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q lacks %s", err, w)
		}
	}
	// Decided, generated, view and excluded columns are not listed.
	for _, no := range []string{"shop.users.id", "shop.users.email", "email_lower", "v_users", "audit"} {
		if strings.Contains(err.Error(), no) {
			t.Errorf("error %q lists %s", err, no)
		}
	}
	// Sorted by database name, then in the order of the catalog.
	idx := func(s string) int { return strings.Index(err.Error(), s) }
	order := []string{"blog.posts.id", "blog.posts.body", "shop.users.note", "shop.orders.id", "shop.orders.total"}
	ordered := true
	for i := 1; i < len(order); i++ {
		ordered = ordered && idx(order[i-1]) < idx(order[i])
	}
	if !ordered {
		t.Errorf("order of %q is not deterministic by database, table, column", err)
	}
}

func TestStrictColumnsPassesWhenAllDecided(t *testing.T) {
	r, err := resolveStrict(t, strictCatalog(), `
version: 1
rules:
  shop.users.*:
    use: keep
  shop.orders.*:
    use: keep
  shop.orders.total:
    use: "null"
  blog.posts.*:
    use: keep
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckStrictColumns(strictCatalog()); err != nil {
		t.Fatalf("CheckStrictColumns: %v", err)
	}
}

func TestStrictColumnsWildcardIsOverriddenByColumnRule(t *testing.T) {
	// The individual rule wins; the wildcard keeps the rest as they are.
	r, err := resolveStrict(t, strictCatalog(), `
version: 1
rules:
  shop.users.*:
    use: keep
  shop.users.email:
    use: fixed
    value: x
`)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	cat := strictCatalog()
	delete(cat.Databases, "blog")
	cat.Databases["shop"] = cat.Databases["shop"][:1] // users only
	if err := r.CheckStrictColumns(cat); err != nil {
		t.Fatalf("CheckStrictColumns: %v", err)
	}
}

func TestStrictColumnsErrorNeverCarriesValues(t *testing.T) {
	r, _ := resolveStrict(t, strictCatalog(), "version: 1\nrules:\n  shop.users.id:\n    use: keep\n")
	err := r.CheckStrictColumns(strictCatalog())
	if err == nil || !strings.Contains(err.Error(), "--strict-columns") || !strings.Contains(err.Error(), "keep") {
		t.Fatalf("err = %v: want it to name the flag and keep", err)
	}
}

func TestWildcardRules(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"other transform", "shop.users.*:\n    use: \"null\"\n", `only "keep" can be used on a wildcard`},
		{"all tables", "shop.*.*:\n    use: keep\n", "table"},
		{"wildcard table, named column", "shop.*.id:\n    use: keep\n", "table"},
		{"no such table", "shop.nope.*:\n    use: keep\n", `no such table "nope"`},
		{"view", "shop.v_users.*:\n    use: keep\n", "is a view"},
		{"excluded", "shop.audit.*:\n    use: keep\n", "is excluded"},
		{"no such database", "nodb.t.*:\n    use: keep\n", `database "nodb"`},
		{"twice", "shop.users.*:\n    use: keep\n  shop.USERS.*:\n    use: keep\n", "already has a wildcard rule"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cat := strictCatalog()
			if c.name == "twice" {
				cat.FoldTableCase = true
			}
			_, err := resolveStrict(t, cat, "version: 1\nrules:\n  "+c.src)
			if err == nil {
				t.Fatal("want error")
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestWildcardWithoutStrictColumnsIsAccepted(t *testing.T) {
	// The wildcard does not need --strict-columns; it changes nothing.
	r, err := resolveStrict(t, strictCatalog(), "version: 1\nrules:\n  shop.users.*:\n    use: keep\n")
	if err != nil {
		t.Fatal(err)
	}
	reg := strictRegistry(t)
	plan, err := r.Resolve(strictCatalog(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Error("a wildcard of keep must plan no transform")
	}
}

func TestWildcardDuplicateOfColumnRuleStaysAllowed(t *testing.T) {
	// A wildcard and a rule for one column are not duplicates: the column wins.
	_, err := resolveStrict(t, strictCatalog(), "version: 1\nrules:\n  shop.users.*:\n    use: keep\n  shop.users.id:\n    use: keep\n")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
