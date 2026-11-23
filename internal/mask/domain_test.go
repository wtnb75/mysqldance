package mask

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

// B-170: a rule may carry a domain; the key of a transform then depends on it.

func TestDeriveDomainKey(t *testing.T) {
	salt := shapeSaltOf(t, shapeSalt)
	base := DeriveKey(*salt, "hash_keep_shape", 1)
	if !bytes.Equal(DeriveDomainKey(*salt, "hash_keep_shape", 1, ""), base) {
		t.Error("an empty domain must give the key of DeriveKey")
	}
	a := DeriveDomainKey(*salt, "hash_keep_shape", 1, "user_id")
	if !bytes.Equal(a, DeriveDomainKey(*salt, "hash_keep_shape", 1, "user_id")) {
		t.Error("the same input gives another key")
	}
	for name, got := range map[string][]byte{
		"no domain":        base,
		"another domain":   DeriveDomainKey(*salt, "hash_keep_shape", 1, "user-id"),
		"case of a domain": DeriveDomainKey(*salt, "hash_keep_shape", 1, "USER_ID"),
		"transform":        DeriveDomainKey(*salt, "other", 1, "user_id"),
		"version":          DeriveDomainKey(*salt, "hash_keep_shape", 2, "user_id"),
		"salt":             DeriveDomainKey(*shapeSaltOf(t, shapeSalt+"x"), "hash_keep_shape", 1, "user_id"),
	} {
		if bytes.Equal(a, got) {
			t.Errorf("%s gives the same key as the domain", name)
		}
	}
	if len(a) != 32 {
		t.Errorf("key is %d bytes", len(a))
	}
}

func TestDomainFormat(t *testing.T) {
	cases := []struct {
		domain string
		ok     bool
	}{
		{"user_id", true},
		{"User-Id", true},
		{"a", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{`""`, false}, // empty
		{"a b", false},
		{"a.b", false},
		{"日本語", false},
	}
	for _, c := range cases {
		t.Run(c.domain, func(t *testing.T) {
			src := "version: 1\nrules:\n  shop.users.name:\n    use: hash_keep_shape\n    domain: " + quoteYAML(c.domain) + "\n"
			_, err := Parse([]byte(src), DefaultRegistry())
			if c.ok && err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !c.ok && (err == nil || !strings.Contains(err.Error(), "domain")) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func quoteYAML(s string) string {
	if s == `""` {
		return s
	}
	return `"` + s + `"`
}

// fkCatalog is a catalog in which child.pid references parent.code (a UNIQUE
// column, since the primary key cannot be transformed), and a composite key.
func fkCatalog() Catalog {
	return Catalog{Databases: map[string][]CatalogTable{"shop": {
		{
			Name: "parent",
			Columns: []CatalogColumn{
				col("id", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", false),
				col("code", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", false),
				col("a", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", false),
				col("b", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", false),
			},
			OrderBy: []string{"id"},
			Indexes: []CatalogIndex{
				{Name: "PRIMARY", Unique: true, Columns: []string{"id"}},
				{Name: "uq_code", Unique: true, Columns: []string{"code"}},
				{Name: "uq_ab", Unique: true, Columns: []string{"a", "b"}},
			},
		},
		{
			Name: "child",
			Columns: []CatalogColumn{
				col("id", "INT", "int", "int", "", false),
				col("pcode", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true),
				col("pa", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true),
				col("pb", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true),
			},
			OrderBy: []string{"id"},
			Indexes: []CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
			ForeignKeys: []CatalogFK{
				{Name: "fk_code", Columns: []string{"pcode"}, RefDB: "shop", RefTable: "parent", RefColumns: []string{"code"}},
				{Name: "fk_ab", Columns: []string{"pa", "pb"}, RefDB: "shop", RefTable: "parent", RefColumns: []string{"a", "b"}},
			},
		},
	}}}
}

func fkRules(lines ...string) string {
	return "version: 1\nrules:\n" + strings.Join(lines, "")
}

func hks(table, column, domain string) string {
	s := "  shop." + table + "." + column + ":\n    use: hash_keep_shape\n"
	if domain != "" {
		s += "    domain: " + domain + "\n"
	}
	return s
}

func TestForeignKeyFollowing(t *testing.T) {
	salt := shapeSaltOf(t, shapeSalt)
	cases := []struct {
		name  string
		rules string
		want  string // "" means allowed
	}{
		{"both sides, same domain", fkRules(hks("parent", "code", "c"), hks("child", "pcode", "c")), ""},
		{"both sides, no domain", fkRules(hks("parent", "code", ""), hks("child", "pcode", "")), ""},
		{"different domain", fkRules(hks("parent", "code", "c"), hks("child", "pcode", "d")), `foreign key "fk_code"`},
		{"domain on one side", fkRules(hks("parent", "code", "c"), hks("child", "pcode", "")), `foreign key "fk_code"`},
		{"child only", fkRules(hks("child", "pcode", "c")), `foreign key "fk_code"`},
		{"parent only", fkRules(hks("parent", "code", "c")), "referenced by a foreign key of table shop.child"},
		{"parent has another transform", fkRules(hks("child", "pcode", ""), "  shop.parent.code:\n    use: fixed\n    value: x\n"), `foreign key "fk_code"`},
		{"composite, both pairs", fkRules(hks("parent", "a", "k"), hks("parent", "b", "k"), hks("child", "pa", "k"), hks("child", "pb", "k")), ""},
		{"composite, different domain in one pair", fkRules(hks("parent", "a", "k"), hks("parent", "b", "k"), hks("child", "pa", "k"), hks("child", "pb", "j")), `foreign key "fk_ab"`},
		{"composite, one pair missing", fkRules(hks("parent", "a", "k"), hks("parent", "b", "k"), hks("child", "pa", "k")), "referenced by a foreign key of table shop.child"},
		{"parent is the primary key", fkRules("  shop.parent.id:\n    use: hash_keep_shape\n"), "part of the key the rows are ordered by"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveRules(t, fkCatalog(), c.rules, salt)
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

// A column that is the parent of one key and the child of another is refused,
// as the ordering key is.
func TestForeignKeyParentThatIsAlsoAChild(t *testing.T) {
	cat := fkCatalog()
	cat.Databases["shop"][0].ForeignKeys = []CatalogFK{{Name: "fk_up", Columns: []string{"code"}, RefDB: "shop", RefTable: "grand", RefColumns: []string{"code"}}}
	cat.Databases["shop"] = append(cat.Databases["shop"], CatalogTable{
		Name:    "grand",
		Columns: []CatalogColumn{col("code", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", false)},
		Indexes: []CatalogIndex{{Name: "uq_code", Unique: true, Columns: []string{"code"}}},
	})
	_, err := resolveRules(t, cat, fkRules(hks("grand", "code", "c"), hks("parent", "code", "c"), hks("child", "pcode", "c")), shapeSaltOf(t, shapeSalt))
	if err == nil || !strings.Contains(err.Error(), `foreign key "fk_up"`) {
		t.Fatalf("err = %v", err)
	}
}

// A foreign key whose referenced columns are fewer than its columns is an error,
// not a panic.
func TestForeignKeyWithMismatchedColumnsIsAnError(t *testing.T) {
	cat := fkCatalog()
	cat.Databases["shop"][1].ForeignKeys[1].RefColumns = []string{"a"}
	_, err := resolveRules(t, cat, fkRules(hks("child", "pb", "k")), shapeSaltOf(t, shapeSalt))
	if err == nil || !strings.Contains(err.Error(), `foreign key "fk_ab"`) {
		t.Fatalf("err = %v", err)
	}
}

// A child whose parent table is not in the dump is told so, not told that the
// parent's rule differs.
func TestForeignKeyChildWhoseParentIsNotDumped(t *testing.T) {
	cat := fkCatalog()
	cat.Databases["shop"][0].Excluded = true
	_, err := resolveRules(t, cat, fkRules(hks("child", "pcode", "c")), shapeSaltOf(t, shapeSalt))
	if err == nil || !strings.Contains(err.Error(), "not in the dump") {
		t.Fatalf("err = %v", err)
	}
}

func TestForeignKeyParentExcludedChildIsNotSeen(t *testing.T) {
	cat := fkCatalog()
	cat.Databases["shop"][1].Excluded = true
	if _, err := resolveRules(t, cat, fkRules(hks("parent", "code", "c")), shapeSaltOf(t, shapeSalt)); err != nil {
		t.Fatalf("a child left out of the dump still blocks: %v", err)
	}
}

func TestDomainOnTransformsWithoutAKey(t *testing.T) {
	for _, rule := range []string{
		"    use: fixed\n    value: x\n",
		"    use: nullify\n",
		"    use: keep\n",
	} {
		_, err := resolveRules(t, builtinCatalog(), "version: 1\nrules:\n  shop.users.name:\n"+rule+"    domain: d\n", nil)
		if err == nil || !strings.Contains(err.Error(), "domain") {
			t.Errorf("%q: err = %v", rule, err)
		}
	}
}

func TestDomainIsNotAllowedUnderPaths(t *testing.T) {
	cat := Catalog{Databases: map[string][]CatalogTable{"shop": {{
		Name:    "docs",
		Columns: []CatalogColumn{col("id", "INT", "int", "int", "", false), col("j", "JSON", "json", "json", "", true)},
		OrderBy: []string{"id"},
		Indexes: []CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}}}}
	for name, src := range map[string]string{
		"on the column": "version: 1\nrules:\n  shop.docs.j:\n    domain: d\n    paths:\n      $.a:\n        use: hash_keep_shape\n",
		"on a path":     "version: 1\nrules:\n  shop.docs.j:\n    paths:\n      $.a:\n        use: hash_keep_shape\n        domain: d\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveRules(t, cat, src, shapeSaltOf(t, shapeSalt))
			if err == nil || !strings.Contains(err.Error(), "domain") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// run masks one value of a column with the plan.
func runColumn(t *testing.T, cat Catalog, rules string, table, column, value string) string {
	t.Helper()
	plan, err := resolveRules(t, cat, rules, shapeSaltOf(t, shapeSalt))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var cols []sqlvalue.Column
	idx := -1
	for _, tb := range cat.Databases["shop"] {
		if tb.Name != table {
			continue
		}
		for i, c := range tb.Columns {
			cols = append(cols, sqlvalue.Column{Name: c.Name, TypeName: c.TypeName})
			if c.Name == column {
				idx = i
			}
		}
	}
	fn, err := plan.Table("shop", table, cols)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]sqlvalue.Value, len(cols))
	for i, c := range cols {
		row[i] = sqlvalue.NewValue(c.TypeName, []byte("0"), false)
	}
	row[idx] = sqlvalue.NewValue(cols[idx].TypeName, []byte(value), false)
	if err := fn(row); err != nil {
		t.Fatal(err)
	}
	return string(row[idx].Bytes())
}

// A child and its parent that share a domain give one value one output, whatever
// the table and the column; another domain, or none, gives another output.
func TestDomainGivesOneValueOneOutputAcrossTables(t *testing.T) {
	const v = "ABCDEFGHIJKLMNOP-0123456789"
	rules := fkRules(hks("parent", "code", "c"), hks("child", "pcode", "c"))
	p := runColumn(t, fkCatalog(), rules, "parent", "code", v)
	c := runColumn(t, fkCatalog(), rules, "child", "pcode", v)
	if p != c {
		t.Errorf("parent %q, child %q: the same domain gave two outputs", p, c)
	}
	other := runColumn(t, fkCatalog(), fkRules(hks("parent", "code", "d"), hks("child", "pcode", "d")), "parent", "code", v)
	none := runColumn(t, fkCatalog(), fkRules(hks("parent", "code", ""), hks("child", "pcode", "")), "parent", "code", v)
	if p == other || p == none || other == none {
		t.Errorf("domains c, d and none gave %q, %q, %q: they must differ", p, other, none)
	}
	// Without a domain the key is the common one, as it was before B-170.
	if got := runColumn(t, shapeCatalog(), shapeRules("v"), "users", "v", v); got != none {
		t.Errorf("no domain gave %q, the common key gave %q", none, got)
	}
}
