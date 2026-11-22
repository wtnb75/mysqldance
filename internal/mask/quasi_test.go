package mask

import (
	"reflect"
	"strings"
	"testing"
)

func parseQuasiErr(t *testing.T, src string) error {
	t.Helper()
	_, err := Parse([]byte(src), strictRegistry(t))
	return err
}

func TestQuasiParsesAloneAndWithUse(t *testing.T) {
	r := strictParse(t, `
version: 1
rules:
  shop.users.email:
    use: keep
    quasi: true
  shop.users.note:
    quasi: true
  shop.orders.total:
    quasi: false
    use: keep
`)
	var got []bool
	for _, e := range r.Entries {
		got = append(got, e.Rule.Quasi)
	}
	if want := []bool{true, true, false}; !reflect.DeepEqual(got, want) {
		t.Errorf("Quasi flags = %v, want %v", got, want)
	}
	if r.Entries[1].Rule.Use != "" {
		t.Errorf("a rule with only quasi must have no transform, got %q", r.Entries[1].Rule.Use)
	}
}

func TestQuasiParseErrors(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"not a bool", "version: 1\nrules:\n  shop.users.email:\n    quasi: yes please\n", "quasi must be a boolean"},
		{"with paths", "version: 1\nrules:\n  shop.users.note:\n    quasi: true\n    paths:\n      $.a:\n        use: keep\n", "quasi cannot be combined with paths"},
		{"inside a path", "version: 1\nrules:\n  shop.users.note:\n    paths:\n      $.a:\n        use: keep\n        quasi: true\n", "quasi cannot be used inside a path rule"},
		{"false alone is no rule", "version: 1\nrules:\n  shop.users.email:\n    quasi: false\n", "use is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseQuasiErr(t, tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestQuasiOnlyRuleChangesNothingInThePlan(t *testing.T) {
	r := strictParse(t, "version: 1\nrules:\n  shop.users.email:\n    quasi: true\n")
	plan, err := r.Resolve(strictCatalog(), strictRegistry(t), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !plan.Empty() {
		t.Errorf("a rule with only quasi must plan no transform")
	}
}

func TestQuasiIsCheckedAgainstTheCatalog(t *testing.T) {
	tests := []struct {
		name, key, want string
	}{
		{"missing column", "shop.users.nope", "no such column"},
		{"generated column", "shop.users.email_lower", "is generated"},
		{"view", "shop.v_users.email", "is a view"},
		{"excluded table", "shop.audit.msg", "is excluded"},
		{"wildcard", "shop.users.*", "cannot be marked quasi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveStrict(t, strictCatalog(), "version: 1\nrules:\n  "+tt.key+":\n    quasi: true\n")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestQuasiDoesNotCountForStrictColumns(t *testing.T) {
	r := strictParse(t, "version: 1\nrules:\n  shop.users.email:\n    quasi: true\n")
	err := r.CheckStrictColumns(strictCatalog())
	if err == nil || !strings.Contains(err.Error(), "shop.users.email") {
		t.Errorf("a quasi-only rule must not decide a column, got %v", err)
	}
}

func TestQuasiTables(t *testing.T) {
	cat := strictCatalog()
	r := strictParse(t, `
version: 1
rules:
  shop.users.note:
    quasi: true
  blog.posts.id:
    quasi: true
  shop.users.id:
    use: keep
    quasi: true
  shop.orders.total:
    use: keep
`)
	got, err := r.QuasiTables(cat, strictRegistry(t))
	if err != nil {
		t.Fatalf("QuasiTables: %v", err)
	}
	// Databases and tables in name order; the columns in the table's own order,
	// not the order of the file.
	want := []QuasiTable{
		{DB: "blog", Table: "posts", Columns: []QuasiColumn{{Name: "id", DataType: ""}}},
		{DB: "shop", Table: "users", Columns: []QuasiColumn{{Name: "id"}, {Name: "note"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestQuasiTablesNeedsAMark(t *testing.T) {
	r := strictParse(t, "version: 1\nrules:\n  shop.users.email:\n    use: keep\n")
	if _, err := r.QuasiTables(strictCatalog(), strictRegistry(t)); err == nil || !strings.Contains(err.Error(), "no column is marked quasi") {
		t.Errorf("error = %v, want one saying no column is marked quasi", err)
	}
}

func TestQuasiTablesReportsRuleErrors(t *testing.T) {
	r := strictParse(t, "version: 1\nrules:\n  shop.users.nope:\n    quasi: true\n")
	if _, err := r.QuasiTables(strictCatalog(), strictRegistry(t)); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Errorf("error = %v, want the error of the rule", err)
	}
}

func TestQuasiTablesFollowsTheCatalogSpelling(t *testing.T) {
	cat := strictCatalog()
	cat.FoldTableCase = true
	r := strictParse(t, "version: 1\nrules:\n  SHOP.Users.EMAIL:\n    quasi: true\n")
	got, err := r.QuasiTables(cat, strictRegistry(t))
	if err != nil {
		t.Fatalf("QuasiTables: %v", err)
	}
	want := []QuasiTable{{DB: "shop", Table: "users", Columns: []QuasiColumn{{Name: "email"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
