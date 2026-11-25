package mask

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"mysqldance/internal/sqlvalue"
)

var docsCols = []sqlvalue.Column{
	{Name: "id", TypeName: "INT"},
	{Name: "doc", TypeName: "JSON"},
	{Name: "req", TypeName: "JSON"},
	{Name: "name", TypeName: "VARCHAR"},
}

// docsRow is a row of the docs table; an empty doc or name makes the column NULL.
func docsRow(doc, name string) []sqlvalue.Value {
	cell := func(typ, s string) sqlvalue.Value {
		if s == "" {
			return sqlvalue.NewValue(typ, nil, true)
		}
		return sqlvalue.NewValue(typ, []byte(s), false)
	}
	return []sqlvalue.Value{
		sqlvalue.NewValue("INT", []byte("1"), false),
		cell("JSON", doc),
		sqlvalue.NewValue("JSON", []byte("{}"), false),
		cell("VARCHAR", name),
	}
}

func usageOf(t *testing.T, rules string) *Plan {
	t.Helper()
	plan, err := resolveRules(t, docsCatalog(), rules, testSalt(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return plan
}

func runRows(t *testing.T, plan *Plan, rows ...[]sqlvalue.Value) {
	t.Helper()
	fn, err := plan.Table("shop", "docs", docsCols)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err := fn(row); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsageCountsRowsWithAValueNotNull(t *testing.T) {
	plan := usageOf(t, "version: 1\nrules:\n  shop.docs.name:\n    use: fixed\n    value: x\n")
	runRows(t, plan, docsRow("", "a"), docsRow("", ""), docsRow("", "b"))
	got := plan.Usage()
	if len(got) != 1 {
		t.Fatalf("usage = %+v, want one rule", got)
	}
	u := got[0]
	if u.DB != "shop" || u.Table != "docs" || u.Column != "name" || u.Use != "fixed" || u.Path != "" {
		t.Errorf("identity = %+v", u)
	}
	if u.Hits != 2 || u.Rows != 3 || u.Unused() {
		t.Errorf("hits %d rows %d unused %v, want 2, 3, false", u.Hits, u.Rows, u.Unused())
	}
}

func TestUsageAllNullIsUnused(t *testing.T) {
	plan := usageOf(t, "version: 1\nrules:\n  shop.docs.name:\n    use: fixed\n    value: x\n")
	runRows(t, plan, docsRow("", ""), docsRow("", ""))
	u := plan.Usage()[0]
	if u.Hits != 0 || u.Rows != 2 || !u.Unused() {
		t.Errorf("hits %d rows %d unused %v, want 0, 2, true", u.Hits, u.Rows, u.Unused())
	}
}

func TestUsageEmptyTableIsNotUnused(t *testing.T) {
	plan := usageOf(t, "version: 1\nrules:\n  shop.docs.name:\n    use: fixed\n    value: x\n")
	runRows(t, plan)
	u := plan.Usage()[0]
	if u.Hits != 0 || u.Rows != 0 || u.Unused() {
		t.Errorf("hits %d rows %d unused %v, want 0, 0, false", u.Hits, u.Rows, u.Unused())
	}
}

func TestUsageSkipsKeepAndQuasi(t *testing.T) {
	plan := usageOf(t, "version: 1\nrules:\n"+
		"  shop.docs.name:\n    use: keep\n"+
		"  shop.docs.id:\n    quasi: true\n"+
		"  shop.docs.req:\n    paths:\n      $.a:\n        use: keep\n")
	runRows(t, plan, docsRow("", "a"))
	if got := plan.Usage(); len(got) != 0 {
		t.Errorf("usage = %+v, want none: keep and quasi cannot miss", got)
	}
}

func TestUsageOfATableWithoutRulesIsEmpty(t *testing.T) {
	var p *Plan
	if got := p.Usage(); len(got) != 0 {
		t.Errorf("nil plan usage = %+v", got)
	}
}

func TestUsageCountsPathsOnePerRowAndPath(t *testing.T) {
	plan := usageOf(t, pathRules("doc",
		"$.a[*]", "use: fixed\nvalue: X",
		"$.b", "use: nullify",
		"$.missing", "use: nullify"))
	runRows(t, plan,
		docsRow(`{"a":["p","q","r"],"b":"x"}`, ""),
		docsRow(`{"a":["p"]}`, ""),
		docsRow("", ""),                 // a NULL column
		docsRow(`{"b":null,"c":1}`, "")) // a JSON null is not changed, so it does not count
	byPath := map[string]RuleUsage{}
	for _, u := range plan.Usage() {
		if u.DB != "shop" || u.Table != "docs" || u.Column != "doc" {
			t.Errorf("identity = %+v", u)
		}
		byPath[u.Path] = u
	}
	want := map[string]struct {
		use  string
		hits int64
	}{"$.a[*]": {"fixed", 2}, "$.b": {"nullify", 1}, "$.missing": {"nullify", 0}}
	if len(byPath) != len(want) {
		t.Fatalf("usage = %+v", byPath)
	}
	for path, w := range want {
		u := byPath[path]
		if u.Use != w.use || u.Hits != w.hits || u.Rows != 4 {
			t.Errorf("%s: use %q hits %d rows %d, want %q %d 4", path, u.Use, u.Hits, u.Rows, w.use, w.hits)
		}
		if u.Unused() != (w.hits == 0) {
			t.Errorf("%s: Unused = %v", path, u.Unused())
		}
	}
}

func TestUsageIsInTheOrderOfTheRulesAndNeverHoldsAValue(t *testing.T) {
	plan := usageOf(t, "version: 1\nrules:\n"+
		"  shop.docs.name:\n    use: fixed\n    value: secret-value\n"+
		"  shop.docs.doc:\n    use: nullify\n")
	runRows(t, plan, docsRow("", "secret-name"))
	first := fmt.Sprintf("%+v", plan.Usage())
	for range 20 {
		if again := fmt.Sprintf("%+v", plan.Usage()); again != first {
			t.Fatalf("usage changed between calls:\n%s\n%s", first, again)
		}
	}
	if strings.Contains(first, "secret") {
		t.Errorf("usage holds a value: %s", first)
	}
}

func TestUsageAddsUpAcrossGoroutines(t *testing.T) {
	plan := usageOf(t, "version: 1\nrules:\n  shop.docs.name:\n    use: fixed\n    value: x\n")
	const workers, perWorker = 8, 500
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			fn, err := plan.Table("shop", "docs", docsCols)
			if err != nil {
				t.Error(err)
				return
			}
			for i := range perWorker {
				name := "n"
				if i%2 == 1 {
					name = ""
				}
				if err := fn(docsRow("", name)); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	u := plan.Usage()[0]
	if u.Rows != workers*perWorker || u.Hits != workers*perWorker/2 {
		t.Errorf("rows %d hits %d, want %d and %d", u.Rows, u.Hits, workers*perWorker, workers*perWorker/2)
	}
}
