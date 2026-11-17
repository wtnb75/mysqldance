package naming

import (
	"sort"
	"strings"
	"testing"
)

func TestEscapeNameCaseSafe(t *testing.T) {
	cases := map[string]string{
		"users":   "users",
		"Users":   "%55sers",
		"USERS":   "%55%53%45%52%53",
		"a_B-c9":  "a_%42-c9",
		"my.Db":   "my%2E%44b",
		"日本Abc":   "%E6%97%A5%E6%9C%AC%41bc",
		"100%Z":   "100%25%5A",
		"":        "",
		"already": "already",
	}
	for in, want := range cases {
		if got := EscapeNameCaseSafe(in); got != want {
			t.Errorf("EscapeNameCaseSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeNameCaseSafeHasNoCaseCollision(t *testing.T) {
	names := []string{"Users", "users", "USERS", "uSers", "a.B", "a.b", "%55sers", "%55SERS", "%2e", "%2E", "é", "É"}
	seen := map[string]string{}
	for _, n := range names {
		k := strings.ToLower(EscapeNameCaseSafe(n))
		if prev, ok := seen[k]; ok {
			t.Errorf("%q and %q collide case-insensitively as %q", prev, n, k)
		}
		seen[k] = n
	}
}

func TestPlanCaseSafeNames(t *testing.T) {
	p := NewPlanCaseSafe("MyDb", []string{"Users", "users"}, true)
	if got, want := p.SchemaFile(), "%4Dy%44b.00-schema.sql"; got != want {
		t.Errorf("SchemaFile = %q, want %q", got, want)
	}
	if got, want := p.DataFile("Users"), "%4Dy%44b.10-data.%55sers.sql"; got != want {
		t.Errorf("DataFile(Users) = %q, want %q", got, want)
	}
	if got, want := p.DataFile("users"), "%4Dy%44b.10-data.users.sql"; got != want {
		t.Errorf("DataFile(users) = %q, want %q", got, want)
	}
	if got, want := p.ConstraintsFile(), "%4Dy%44b.90-constraints.sql"; got != want {
		t.Errorf("ConstraintsFile = %q, want %q", got, want)
	}
	if got := p.DataFileParts("Users", 2); got[0] != "%4Dy%44b.10-data.%55sers.0001.sql" {
		t.Errorf("DataFileParts = %q", got)
	}
}

func TestPlanDefaultIsNotCaseSafe(t *testing.T) {
	a := NewPlan("MyDb", []string{"Users"})
	b := NewPlanCaseSafe("MyDb", []string{"Users"}, false)
	if a.DataFile("Users") != "MyDb.10-data.Users.sql" || b.DataFile("Users") != a.DataFile("Users") {
		t.Errorf("default names changed: %q, %q", a.DataFile("Users"), b.DataFile("Users"))
	}
}

func TestPlanCaseSafeSchemaSortsBeforeData(t *testing.T) {
	p := NewPlanCaseSafe("MyDb", []string{"A", "b", "Zed", "~"}, true)
	files := []string{p.ConstraintsFile(), p.TriggersFile(), p.EventsFile(), p.ViewsFile(), p.RoutinesFile()}
	for _, tb := range []string{"A", "b", "Zed", "~"} {
		files = append(files, p.DataFile(tb))
	}
	files = append(files, p.SchemaFile())
	sort.Strings(files)
	if files[0] != p.SchemaFile() {
		t.Errorf("schema file is not first: %q", files)
	}
	if files[len(files)-1] != p.ConstraintsFile() {
		t.Errorf("constraints file is not last: %q", files)
	}
}

// A run owns the files of its database in both forms, so switching the option
// leaves no file of an earlier dump behind.
func TestPlanOwnsBothForms(t *testing.T) {
	plain := NewPlan("MyDb", []string{"Users"})
	safe := NewPlanCaseSafe("MyDb", []string{"Users"}, true)
	for _, p := range []*Plan{plain, safe} {
		for _, n := range []string{plain.SchemaFile(), plain.DataFile("Users"), plain.ConstraintsFile(),
			safe.SchemaFile(), safe.DataFile("Users"), safe.TriggersFile()} {
			if !p.IsOwned(n) {
				t.Errorf("%s is not owned", n)
			}
		}
		if p.IsOwned("%4Dy%44bx.00-schema.sql") || p.IsOwned("mydb.00-schema.sql") || p.IsOwned("Other.00-schema.sql") {
			t.Error("a file of another database is owned")
		}
	}
}

func TestPlanOwnsBothFormsOfShortenedName(t *testing.T) {
	db := strings.Repeat("Ab", 100)
	tbl := strings.Repeat("c", 60)
	plain := NewPlan(db, []string{tbl})
	safe := NewPlanCaseSafe(db, []string{tbl}, true)
	if !plain.DBShortened() || !safe.DBShortened() {
		t.Fatal("the database part was not shortened")
	}
	for _, n := range []string{plain.SchemaFile(), safe.SchemaFile(), safe.DataFile(tbl)} {
		if len(n) > MaxFileNameBytes {
			t.Errorf("%d bytes: %s", len(n), n)
		}
		if !plain.IsOwned(n) || !safe.IsOwned(n) {
			t.Errorf("%s is not owned by both plans", n)
		}
	}
}

func TestPlanCaseSafeShortenedNameStaysWithinLimit(t *testing.T) {
	tbl := strings.Repeat("Z", 100) // 300 bytes once escaped
	p := NewPlanCaseSafe("d", []string{tbl}, true)
	n := p.DataFile(tbl)
	if len(n) > MaxFileNameBytes {
		t.Errorf("%d bytes: %s", len(n), n)
	}
	if !p.TableShortened(tbl, 1) {
		t.Error("the table part should be shortened")
	}
}
