package mask

import (
	"regexp"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

func emailRules(column, extra string) string {
	return "version: 1\nrules:\n  shop.users." + column + ":\n    use: hash_email\n" + extra
}

// emailRunner returns a function that masks one value of the column v with the
// rule given (extra is more lines of the rule, such as "    host: a.invalid\n").
func emailRunner(t *testing.T, salt, extra string) func(string) (string, error) {
	t.Helper()
	plan, err := resolveRules(t, shapeCatalog(), emailRules("v", extra), shapeSaltOf(t, salt))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var cols []sqlvalue.Column
	idx := -1
	for i, c := range shapeCatalog().Databases["shop"][0].Columns {
		cols = append(cols, sqlvalue.Column{Name: c.Name, TypeName: c.TypeName})
		if c.Name == "v" {
			idx = i
		}
	}
	fn, err := plan.Table("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	return func(s string) (string, error) {
		row := make([]sqlvalue.Value, len(cols))
		for i, c := range cols {
			row[i] = sqlvalue.NewValue(c.TypeName, []byte("0"), false)
		}
		row[idx] = sqlvalue.NewValue("VARCHAR", []byte(s), false)
		if err := fn(row); err != nil {
			return "", err
		}
		return string(row[idx].Bytes()), nil
	}
}

func TestHashEmailIsRegistered(t *testing.T) {
	tr, ok := DefaultRegistry().Lookup("hash_email")
	if !ok {
		t.Fatal("hash_email is not registered")
	}
	if tr.Version != 1 || !tr.NeedsSalt || !tr.BreaksKeys || !tr.UniqueChecked {
		t.Errorf("transform = %+v", tr)
	}
	if len(tr.Args) != 1 || tr.Args[0].Name != "host" || tr.Args[0].Kind != ArgString || tr.Args[0].Required {
		t.Errorf("args = %+v", tr.Args)
	}
	if tr.BuildLeaf != nil {
		t.Error("hash_email must not be usable under a JSON path")
	}
}

func TestHashEmailColumns(t *testing.T) {
	salt := shapeSaltOf(t, shapeSalt)
	for _, c := range []string{"v", "u3", "l1", "t", "code"} {
		if _, err := resolveRules(t, shapeCatalog(), emailRules(c, ""), salt); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	for _, c := range []struct{ column, want string }{{"n", "does not apply"}, {"b", "does not apply"}, {"id", "key"}} {
		_, err := resolveRules(t, shapeCatalog(), emailRules(c.column, ""), salt)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), c.want) {
			t.Errorf("%s: err = %v, want %q", c.column, err, c.want)
		}
	}
}

func TestHashEmailNeedsSalt(t *testing.T) {
	_, err := resolveRules(t, shapeCatalog(), emailRules("v", ""), nil)
	if err == nil || !strings.Contains(err.Error(), "needs a salt") {
		t.Fatalf("err = %v", err)
	}
}

func TestHashEmailHostFormat(t *testing.T) {
	salt := shapeSaltOf(t, shapeSalt)
	good := []string{"example.invalid", "a", "Mail-1.example.com", strings.Repeat("a", 253)}
	for _, h := range good {
		if _, err := resolveRules(t, shapeCatalog(), emailRules("v", "    host: "+h+"\n"), salt); err != nil {
			t.Errorf("host %q: %v", h, err)
		}
	}
	bad := []string{`"a@b"`, `"a b"`, `""`, `"é.invalid"`, `"a_b.invalid"`, `"` + strings.Repeat("a", 254) + `"`}
	for _, h := range bad {
		_, err := resolveRules(t, shapeCatalog(), emailRules("v", "    host: "+h+"\n"), salt)
		if err == nil || !strings.Contains(err.Error(), "host") {
			t.Errorf("host %s: err = %v", h, err)
		}
	}
}

func TestHashEmailHostMustBeAString(t *testing.T) {
	for _, h := range []string{"5", "true"} {
		src := emailRules("v", "    host: "+h+"\n")
		if _, err := Parse([]byte(src), DefaultRegistry()); err == nil || !strings.Contains(err.Error(), "host") {
			t.Errorf("host %s: err = %v", h, err)
		}
	}
}

func TestHashEmailRefusesUnsupportedKeys(t *testing.T) {
	salt := shapeSaltOf(t, shapeSalt)
	for _, extra := range []string{"    domain: d\n", "    when: id > 1\n", "    on_null: keep\n"} {
		_, err := resolveRules(t, shapeCatalog(), emailRules("v", extra), salt)
		if err == nil {
			t.Errorf("%q: no error", extra)
		}
	}
}

func TestHashEmailIsNotUsableUnderAPath(t *testing.T) {
	cat := Catalog{Databases: map[string][]CatalogTable{"shop": {{
		Name:    "docs",
		Columns: []CatalogColumn{col("id", "INT", "int", "int", "", false), col("j", "JSON", "json", "json", "", true)},
		OrderBy: []string{"id"},
		Indexes: []CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}}}}
	src := "version: 1\nrules:\n  shop.docs.j:\n    paths:\n      $.a:\n        use: hash_email\n"
	if _, err := resolveRules(t, cat, src, shapeSaltOf(t, shapeSalt)); err == nil {
		t.Fatal("hash_email under a JSON path was accepted")
	}
}

func TestHashEmailShapeAndLength(t *testing.T) {
	run := emailRunner(t, shapeSalt, "")
	shape := regexp.MustCompile(`^[a-z0-9]+@example\.invalid$`)
	for _, in := range []string{
		"alice@example.com",
		"bob.smith+tag@very-long-domain-name.example.org",
		"x@a.b.c.d.e.f.g.h.i.j.k.l.m.n.o.p.q.r.s.t.u.v.w.x.y.z.example.co.jp",
		"山田太郎@example.co.jp", // non-ASCII local part: bytes are kept, not characters
		"a@b@c.example.com",  // the last @ splits
		"UPPER.Case@Example.COM",
	} {
		out, err := run(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if len(out) != len(in) {
			t.Errorf("%q: length %d, want %d (%q)", in, len(out), len(in), out)
		}
		if !shape.MatchString(out) {
			t.Errorf("%q: %q does not match %v", in, out, shape)
		}
	}
}

func TestHashEmailIsDeterministic(t *testing.T) {
	a := emailRunner(t, shapeSalt, "")
	b := emailRunner(t, shapeSalt, "")
	other := emailRunner(t, shapeSalt+"x", "")
	in := "alice@example.com"
	x, _ := a(in)
	if y, _ := b(in); x != y {
		t.Errorf("two runs differ: %q %q", x, y)
	}
	if y, _ := a(in); x != y {
		t.Errorf("one run differs: %q %q", x, y)
	}
	if y, _ := other(in); x == y {
		t.Errorf("another salt gives the same value %q", x)
	}
	if y, _ := a("bob@example.com"); x == y {
		t.Errorf("two addresses gave one value %q", x)
	}
}

func TestHashEmailHostIsWhatTheRuleSays(t *testing.T) {
	run := emailRunner(t, shapeSalt, "    host: test.example.com\n")
	in := "alice@corp.example.org"
	out, err := run(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "@test.example.com") || len(out) != len(in) {
		t.Errorf("out = %q", out)
	}
}

func TestHashEmailLengthWhenTheDomainIsLongerOrShorter(t *testing.T) {
	run := emailRunner(t, shapeSalt, "")
	for _, in := range []string{"abcdefghijklmnop@x.io", "ab@a-very-long-domain-name.example.com", "abcdef@example.invalid", "abcdefghijklmnop@x"} {
		out, err := run(in)
		if err != nil || len(out) != len(in) {
			t.Errorf("%q: out = %q, err = %v", in, out, err)
		}
	}
}

func TestHashEmailErrorsHoldNoValue(t *testing.T) {
	run := emailRunner(t, shapeSalt, "")
	// "a@b.io" is 6 bytes: N = 1 + 4 - 15 < 1.
	for _, in := range []string{"a@b.io", "secret-no-at-sign", "secret@x.y"} {
		_, err := run(in)
		if err == nil {
			t.Errorf("%q: no error", in)
			continue
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), in) {
			t.Errorf("the error holds the value: %v", err)
		}
		if !strings.Contains(err.Error(), "users") || !strings.Contains(strings.ToLower(err.Error()), "varchar") {
			t.Errorf("the error names no column or type: %v", err)
		}
	}
}

func TestHashEmailKeepsNullAndEmpty(t *testing.T) {
	run := emailRunner(t, shapeSalt, "")
	if out, err := run(""); err != nil || out != "" {
		t.Errorf("empty: %q, %v", out, err)
	}
	fn, err := buildHashEmail(BuildContext{Key: []byte("k"), Column: Column{Name: "v", ColumnType: "varchar(40)"}})
	if err != nil {
		t.Fatal(err)
	}
	null := sqlvalue.NewValue("VARCHAR", nil, true)
	if out, err := fn(Input{Value: null}); err != nil || !out.IsNull() {
		t.Errorf("NULL: %v, %v", out, err)
	}
}

func TestHashEmailMinimumLocalPart(t *testing.T) {
	// N = L + D - len(host) = 1: the shortest address that can be written.
	run := emailRunner(t, shapeSalt, "    host: a.io\n")
	out, err := run("a@x.io") // L=1, D=4, len(host)=4: N=1
	if err != nil || len(out) != 6 {
		t.Errorf("out = %q, err = %v", out, err)
	}
	if _, err := run("a@x.i"); err == nil { // N = 0
		t.Error("N = 0 was accepted")
	}
}
