package mask

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testRegistry registers transforms that exist only for these tests.
func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry()
	for _, tr := range []Transform{
		{Name: "fixed", Args: []ArgDef{{Name: "value", Kind: ArgString, Required: true}}, Types: []string{"VARCHAR", "CHAR", "TEXT"}},
		{Name: "null"},
		{Name: "shift", Args: []ArgDef{{Name: "days", Kind: ArgInt}, {Name: "keep", Kind: ArgBool}}, Types: []string{"DATE", "DATETIME"}},
		{Name: "opaque", Args: []ArgDef{{Name: "options", Kind: ArgAny}}},
	} {
		if err := reg.Register(tr); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func parseOK(t *testing.T, src string) *Rules {
	t.Helper()
	r, err := Parse([]byte(src), testRegistry(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func parseErr(t *testing.T, src string, want ...string) {
	t.Helper()
	_, err := Parse([]byte(src), testRegistry(t))
	if err == nil {
		t.Fatalf("Parse succeeded, want error with %q", want)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q lacks %q", err, w)
		}
	}
}

func TestParseValid(t *testing.T) {
	r := parseOK(t, `
version: 1
rules:
  shop.users.email:
    use: fixed
    value: x@example.invalid
    domain: mail
    when: "id > 1"
    on_null: keep
    on_collision: error
  shop.users.profile:
    paths:
      $.phone:
        use: fixed
        value: "000"
      $.tags[*]:
        use: "null"
  shop.my.tbl.created:
    use: shift
    days: 3
    keep: true
`)
	if len(r.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(r.Entries))
	}
	e := r.Entries[0]
	if e.Key != (Key{DB: "shop", Table: "users", Column: "email"}) || e.Rule.Use != "fixed" ||
		e.Rule.Args["value"] != "x@example.invalid" || e.Rule.Domain != "mail" || e.Rule.When != "id > 1" ||
		e.Rule.OnNull != "keep" || e.Rule.OnCollision != "error" {
		t.Errorf("entry 0 = %+v", e)
	}
	if got := r.Entries[1].Rule.Paths; len(got) != 2 || got[0].Path != "$.phone" || got[1].Rule.Use != "null" {
		t.Errorf("paths = %+v", got)
	}
	if k := r.Entries[2].Key; k.Table != "my.tbl" || k.Column != "created" {
		t.Errorf("dotted table key = %+v", k)
	}
	if r.Entries[2].Rule.Args["days"] != int64(3) || r.Entries[2].Rule.Args["keep"] != true {
		t.Errorf("args = %+v", r.Entries[2].Rule.Args)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"empty", "", []string{"empty"}},
		{"not yaml", "version: [1", []string{"invalid YAML"}},
		{"two documents", "version: 1\nrules: {}\n---\nversion: 1\n", []string{"more than one"}},
		{"missing version", "rules: {}\n", []string{"version is required"}},
		{"missing rules", "version: 1\n", []string{"rules is required"}},
		{"wrong version", "version: 2\nrules: {}\n", []string{"unsupported version"}},
		{"string version", "version: \"1\"\nrules: {}\n", []string{"unsupported version"}},
		{"unknown top-level key", "version: 1\nrules: {}\nextra: 1\n", []string{`unknown key "extra"`}},
		{"duplicate top-level key", "version: 1\nversion: 1\nrules: {}\n", []string{`appears twice`}},
		{"bad key form", "version: 1\nrules:\n  a.b:\n    use: \"null\"\n", []string{"DB.TABLE.COLUMN"}},
		{"empty key part", "version: 1\nrules:\n  a..c:\n    use: \"null\"\n", []string{"DB.TABLE.COLUMN"}},
		{"duplicate rule key", "version: 1\nrules:\n  a.b.c:\n    use: \"null\"\n  a.b.c:\n    use: \"null\"\n", []string{`appears twice`}},
		{"unknown transform", "version: 1\nrules:\n  a.b.c:\n    use: nope\n", []string{`unknown transform "nope"`, "fixed"}},
		{"unknown argument", "version: 1\nrules:\n  a.b.c:\n    use: fixed\n    value: x\n    bogus: 1\n", []string{`no argument "bogus"`}},
		{"unknown key without use", "version: 1\nrules:\n  a.b.c:\n    bogus: 1\n", []string{`unknown key "bogus"`}},
		{"missing required argument", "version: 1\nrules:\n  a.b.c:\n    use: fixed\n", []string{`needs argument "value"`}},
		{"argument of the wrong kind", "version: 1\nrules:\n  a.b.c:\n    use: shift\n    days: three\n", []string{`"days" must be a integer`}},
		{"string argument given a number", "version: 1\nrules:\n  a.b.c:\n    use: fixed\n    value: 12\n", []string{`"value" must be a string`}},
		{"no use", "version: 1\nrules:\n  a.b.c:\n    domain: d\n", []string{"use is required"}},
		{"use with paths", "version: 1\nrules:\n  a.b.c:\n    use: \"null\"\n    paths:\n      $.x:\n        use: \"null\"\n", []string{"cannot be combined"}},
		{"common key not a string", "version: 1\nrules:\n  a.b.c:\n    use: \"null\"\n    domain: [1]\n", []string{"domain must be a string"}},
		{"rule not a mapping", "version: 1\nrules:\n  a.b.c: \"null\"\n", []string{"must be a mapping"}},
		{"integer out of range", "version: 1\nrules:\n  a.b.c:\n    use: shift\n    days: 99999999999999999999\n", []string{`"days" must be a integer`}},
		{"empty path", "version: 1\nrules:\n  a.b.c:\n    paths:\n      \"\":\n        use: \"null\"\n", []string{"path must not be empty"}},
		{"nested paths", "version: 1\nrules:\n  a.b.c:\n    paths:\n      $.x:\n        paths:\n          $.y:\n            use: \"null\"\n", []string{"cannot be nested"}},
		{"nested unknown transform", "version: 1\nrules:\n  a.b.c:\n    paths:\n      $.x:\n        use: nope\n", []string{`unknown transform "nope"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { parseErr(t, c.src, c.want...) })
	}
}

func TestParseReportsAllProblems(t *testing.T) {
	_, err := Parse([]byte("version: 1\nrules:\n  a.b.c:\n    use: nope\n  x.y:\n    use: \"null\"\nextra: 1\n"), testRegistry(t))
	if err == nil {
		t.Fatal("want error")
	}
	for _, w := range []string{`unknown transform "nope"`, "DB.TABLE.COLUMN", `unknown key "extra"`} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q lacks %q", err, w)
		}
	}
}

func TestSplitKey(t *testing.T) {
	good := map[string]Key{
		"d.t.c":     {"d", "t", "c"},
		"d.t.u.c":   {"d", "t.u", "c"},
		"db.t..c":   {"db", "t.", "c"},
		"d.a.b.c.e": {"d", "a.b.c", "e"},
	}
	for in, want := range good {
		got, err := SplitKey(in)
		if err != nil || got != want {
			t.Errorf("SplitKey(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "d.t", ".t.c", "d..c", "d.t.", "..", "..."} {
		if k, err := SplitKey(in); err == nil {
			t.Errorf("SplitKey(%q) = %+v, want error", in, k)
		}
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Transform{Name: ""}); err == nil {
		t.Error("empty name accepted")
	}
	if err := reg.Register(Transform{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(Transform{Name: "a"}); err == nil {
		t.Error("duplicate name accepted")
	}
	if err := reg.Register(Transform{Name: "b", Args: []ArgDef{{Name: "when"}}}); err == nil {
		t.Error("argument named like a common key accepted")
	}
	if err := reg.Register(Transform{Name: "c", Args: []ArgDef{{Name: "x"}, {Name: "x"}}}); err == nil {
		t.Error("argument declared twice accepted")
	}
	if got := strings.Join(reg.Names(), ","); got != "a" {
		t.Errorf("Names = %q", got)
	}
}

func TestRegistryTypesNormalized(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Transform{Name: "x", Types: []string{"varchar", "unsigned int"}}); err != nil {
		t.Fatal(err)
	}
	tr, _ := reg.Lookup("x")
	for _, ty := range []string{"VARCHAR", "varchar", " VARCHAR ", "INT", "UNSIGNED INT"} {
		if !tr.appliesTo(ty) {
			t.Errorf("does not apply to %q", ty)
		}
	}
	if tr.appliesTo("TEXT") {
		t.Error("applies to TEXT")
	}
}

func TestNilRegistry(t *testing.T) {
	_, err := Parse([]byte("version: 1\nrules:\n  a.b.c:\n    use: x\n"), nil)
	if err == nil || !strings.Contains(err.Error(), `unknown transform "x"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateDatabaseCaseFollowsServer(t *testing.T) {
	src := "version: 1\nrules:\n  Shop.users.email:\n    use: \"null\"\n"
	if err := validate(t, testCatalog(), src); err == nil {
		t.Fatal("database name matched without folding")
	}
	cat := testCatalog()
	cat.FoldTableCase = true
	if err := validate(t, cat, src); err != nil {
		t.Fatalf("folded database name rejected: %v", err)
	}
	dup := src + "  shop.USERS.email:\n    use: \"null\"\n"
	if err := validate(t, cat, dup); err == nil || !strings.Contains(err.Error(), "already has a rule") {
		t.Fatalf("duplicate by database case: err = %v", err)
	}
}

func testCatalog() Catalog {
	return Catalog{
		Databases: map[string][]CatalogTable{
			"shop": {
				{Name: "users", Columns: []CatalogColumn{
					{Name: "id", TypeName: "UNSIGNED INT"},
					{Name: "Email", TypeName: "VARCHAR"},
					{Name: "born", TypeName: "DATE"},
					{Name: "profile", TypeName: "JSON"},
					{Name: "email_lower", TypeName: "VARCHAR", Generated: true},
				}},
				{Name: "Orders", Columns: []CatalogColumn{{Name: "note", TypeName: "TEXT"}}},
				{Name: "v_users", View: true, Columns: []CatalogColumn{{Name: "email", TypeName: "VARCHAR"}}},
				{Name: "audit", Excluded: true, Columns: []CatalogColumn{{Name: "msg", TypeName: "TEXT"}}},
			},
		},
	}
}

func validate(t *testing.T, cat Catalog, src string) error {
	t.Helper()
	reg := testRegistry(t)
	r, err := Parse([]byte(src), reg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r.Validate(cat, reg)
}

func TestValidateOK(t *testing.T) {
	err := validate(t, testCatalog(), `
version: 1
rules:
  shop.users.email:
    use: fixed
    value: x
  shop.users.born:
    use: shift
    days: 1
  shop.Orders.note:
    use: "null"
`)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// B-166: the keys the parser reads but gives no meaning yet are errors, not
// ignored, so that a rule never masks less than it says.
func TestValidateRefusesKeysWithoutMeaning(t *testing.T) {
	cases := map[string]string{
		"domain":       "    domain: mail\n",
		"when":         "    when: x\n",
		"on_null":      "    on_null: keep\n",
		"on_collision": "    on_collision: error\n",
	}
	for key, line := range cases {
		t.Run(key, func(t *testing.T) {
			err := validate(t, testCatalog(), "version: 1\nrules:\n  shop.users.email:\n    use: fixed\n    value: x\n"+line)
			if err == nil || !strings.Contains(err.Error(), key+" is not supported in this version") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		rule string
		want string
	}{
		{"unknown database", "nodb.users.email", `database "nodb" is not one of the databases to dump`},
		{"unknown table", "shop.nope.email", `no such table "nope"`},
		{"table case differs", "shop.orders.note", `no such table "orders"`},
		{"unknown column", "shop.users.nope", `no such column "nope"`},
		{"view", "shop.v_users.email", "is a view"},
		{"excluded table", "shop.audit.msg", "is excluded"},
		{"generated column", "shop.users.email_lower", "is generated"},
		{"type mismatch", "shop.users.id", `does not apply to column "id" of type UNSIGNED INT`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "version: 1\nrules:\n  " + c.rule + ":\n    use: fixed\n    value: x\n"
			err := validate(t, testCatalog(), src)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestValidatePathsNeedJSONColumn(t *testing.T) {
	err := validate(t, testCatalog(), "version: 1\nrules:\n  shop.users.email:\n    paths:\n      $.a:\n        use: \"null\"\n")
	if err == nil || !strings.Contains(err.Error(), "JSON column") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateColumnCaseInsensitive(t *testing.T) {
	err := validate(t, testCatalog(), "version: 1\nrules:\n  shop.users.EMAIL:\n    use: \"null\"\n")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateDuplicates(t *testing.T) {
	err := validate(t, testCatalog(), "version: 1\nrules:\n  shop.users.email:\n    use: \"null\"\n  shop.users.EMAIL:\n    use: \"null\"\n")
	if err == nil || !strings.Contains(err.Error(), "already has a rule") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTableCaseFollowsServer(t *testing.T) {
	cat := testCatalog()
	cat.FoldTableCase = true
	err := validate(t, cat, "version: 1\nrules:\n  shop.orders.note:\n    use: \"null\"\n")
	if err != nil {
		t.Fatalf("folded table name rejected: %v", err)
	}
	err = validate(t, cat, "version: 1\nrules:\n  shop.orders.note:\n    use: \"null\"\n  shop.ORDERS.NOTE:\n    use: \"null\"\n")
	if err == nil || !strings.Contains(err.Error(), "already has a rule") {
		t.Fatalf("duplicate by table case: err = %v", err)
	}
	// Without folding, the two spellings are two tables, and the second is missing.
	err = validate(t, testCatalog(), "version: 1\nrules:\n  shop.Orders.note:\n    use: \"null\"\n  shop.ORDERS.note:\n    use: \"null\"\n")
	if err == nil || strings.Contains(err.Error(), "already has a rule") {
		t.Fatalf("exact table names: err = %v", err)
	}
}

func TestValidateReportsAll(t *testing.T) {
	err := validate(t, testCatalog(), "version: 1\nrules:\n  nodb.a.b:\n    use: \"null\"\n  shop.nope.c:\n    use: \"null\"\n  shop.users.id:\n    use: fixed\n    value: x\n")
	if err == nil {
		t.Fatal("want error")
	}
	if n := strings.Count(err.Error(), "\n") + 1; n != 3 {
		t.Errorf("%d errors, want 3: %v", n, err)
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nrules:\n  a.b.c:\n    use: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, testRegistry(t))
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want it to name the file", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), testRegistry(t)); err == nil {
		t.Fatal("missing file accepted")
	}
}

const goodSalt = "0123456789abcdef-secret"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeSalt(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "salt")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSaltFromEnv(t *testing.T) {
	s, err := LoadSalt("", env(map[string]string{SaltEnv: goodSalt}))
	if err != nil || string(s.Bytes()) != goodSalt {
		t.Fatalf("salt = %q, %v", s.Bytes(), err)
	}
}

func TestLoadSaltFromFile(t *testing.T) {
	var s Salt
	var err error
	for _, content := range []string{goodSalt, goodSalt + "\n", goodSalt + "\r\n"} {
		s, err = LoadSalt(writeSalt(t, content, 0o600), env(nil))
		if err != nil || string(s.Bytes()) != goodSalt {
			t.Errorf("content %q: salt = %q, %v", content, s.Bytes(), err)
		}
	}
	// A lone carriage return is part of the salt; only "\n" and "\r\n" are cut.
	s, err = LoadSalt(writeSalt(t, goodSalt+"\r", 0o600), env(nil))
	if err != nil || string(s.Bytes()) != goodSalt+"\r" {
		t.Errorf("lone CR: salt = %q, %v", s.Bytes(), err)
	}
	// Only one newline is cut.
	s, err = LoadSalt(writeSalt(t, goodSalt+"\n\n", 0o600), env(nil))
	if err != nil || string(s.Bytes()) != goodSalt+"\n" {
		t.Errorf("two newlines: salt = %q, %v", s.Bytes(), err)
	}
}

func TestLoadSaltFilePreferredOverEnv(t *testing.T) {
	s, err := LoadSalt(writeSalt(t, "file-salt-0123456789", 0o600), env(map[string]string{SaltEnv: goodSalt}))
	if err != nil || string(s.Bytes()) != "file-salt-0123456789" {
		t.Fatalf("salt = %q, %v", s.Bytes(), err)
	}
}

func TestLoadSaltRejects(t *testing.T) {
	short := "short-secret-15!" // 16 bytes, trimmed below
	short = short[:15]
	cases := []struct {
		name string
		file string
		env  map[string]string
		want string
	}{
		{"unset", "", nil, "no salt"},
		{"empty env is unset", "", map[string]string{SaltEnv: ""}, "no salt"},
		{"short env", "", map[string]string{SaltEnv: short}, "at least 16 bytes"},
		{"empty file", writeSalt(t, "", 0o600), nil, "empty"},
		{"newline only file", writeSalt(t, "\n", 0o600), nil, "empty"},
		{"short file", writeSalt(t, short+"\n", 0o600), nil, "at least 16 bytes"},
		{"missing file", filepath.Join(t.TempDir(), "none"), nil, "salt file"},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases,
			struct {
				name, file string
				env        map[string]string
				want       string
			}{"group readable", writeSalt(t, goodSalt, 0o640), nil, "by group or others"},
			struct {
				name, file string
				env        map[string]string
				want       string
			}{"world readable", writeSalt(t, goodSalt, 0o604), nil, "by group or others"},
			struct {
				name, file string
				env        map[string]string
				want       string
			}{"group writable", writeSalt(t, goodSalt, 0o620), nil, "by group or others"},
		)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadSalt(c.file, env(c.env))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A file that is too open fails even when the environment holds a good salt.
func TestLoadSaltOpenFileFailsDespiteEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits carry no access rules")
	}
	_, err := LoadSalt(writeSalt(t, goodSalt, 0o644), env(map[string]string{SaltEnv: goodSalt}))
	if err == nil {
		t.Fatal("want error")
	}
}

func TestSaltNeverPrinted(t *testing.T) {
	s, err := LoadSalt("", env(map[string]string{SaltEnv: goodSalt}))
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{
		fmt.Sprint(s), fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s), fmt.Sprintf("%10s", s), fmt.Sprintf("%q", s),
	} {
		if strings.Contains(out, "secret") || strings.Contains(out, "0123456789abcdef") {
			t.Errorf("salt printed: %q", out)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "salt", s)
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("salt logged: %q", buf.String())
	}
}

func TestSaltErrorsNeverCarryValue(t *testing.T) {
	_, err := LoadSalt("", env(map[string]string{SaltEnv: "tooshort-secret"}))
	if err == nil || strings.Contains(err.Error(), "tooshort-secret") {
		t.Fatalf("err = %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	_, err = LoadSalt(writeSalt(t, goodSalt, 0o644), env(nil))
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}
