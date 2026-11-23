package mask

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

func TestParseJSONPath(t *testing.T) {
	good := []struct {
		path string
		want string // the steps, written k:key, i:n, *
	}{
		{"$.a", "k:a"},
		{"$.profile.email", "k:profile k:email"},
		{"$._x1.Y_2", "k:_x1 k:Y_2"},
		{"$.phones[*]", "k:phones *"},
		{"$.phones[0]", "k:phones i:0"},
		{"$.phones[12].n", "k:phones i:12 k:n"},
		{`$["a b"]`, "k:a b"},
		{`$["a\"b\\c"]`, `k:a"b\c`},
		{`$["日本語"]`, "k:日本語"},
		{`$["\u00e9"]`, "k:é"},
		{"$[*][0]", "* i:0"},
		{`$.a["1x"][*]`, "k:a k:1x *"},
	}
	for _, c := range good {
		t.Run(c.path, func(t *testing.T) {
			steps, err := parseJSONPath(c.path)
			if err != nil {
				t.Fatalf("parseJSONPath: %v", err)
			}
			if got := describeSteps(steps); got != c.want {
				t.Errorf("steps = %q, want %q", got, c.want)
			}
		})
	}
	bad := []struct{ path, want string }{
		{"$", "must point inside"},
		{"", "start with"},
		{"a.b", "start with"},
		{"$a", "unexpected"},
		{"$.", "key"},
		{"$.1a", "key"},
		{"$.a-b", "unexpected"},
		{"$.a b", "unexpected"},
		{"$..a", "recursive"},
		{"$.a..b", "recursive"},
		{"$.a[-1]", "negative"},
		{"$.a[1:2]", "slice"},
		{"$.a[?(@.x)]", "filter"},
		{"$.a[", "unterminated"},
		{"$.a[0", "unterminated"},
		{"$.a[x]", "unexpected"},
		{"$.a[01]", "leading zero"},
		{"$.a[99999999999]", "too large"},
		{`$["a]`, "unterminated"},
		{`$["a"`, "unterminated"},
		{`$['a']`, "double quotes"},
		{"$.a[*", "unterminated"},
		{"$.a.*", "key"},
	}
	for _, c := range bad {
		t.Run("bad "+c.path, func(t *testing.T) {
			_, err := parseJSONPath(c.path)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func describeSteps(steps []pathStep) string {
	var parts []string
	for _, s := range steps {
		switch s.kind {
		case stepKey:
			parts = append(parts, "k:"+s.key)
		case stepIndex:
			parts = append(parts, "i:"+strconv.Itoa(s.index))
		default:
			parts = append(parts, "*")
		}
	}
	return strings.Join(parts, " ")
}

func TestPathsOverlap(t *testing.T) {
	cases := []struct {
		a, b    string
		overlap bool
	}{
		{"$.a", "$.a", true},
		{"$.a", "$.a.b", true},
		{"$.a.b", "$.a", true},
		{"$.p[*]", "$.p[0]", true},
		{"$.p[0]", "$.p[*]", true},
		{"$.p[*]", "$.p[*].x", true},
		{`$["a"]`, "$.a", true},
		{"$.a", "$.b", false},
		{"$.p[0]", "$.p[1]", false},
		{"$.p[*].x", "$.p[0].y", false},
		{"$.a", "$[0]", false},
		{"$[*][0].x", "$[0][*].y", false},
		{"$[*][0]", "$[0][*]", true},
	}
	for _, c := range cases {
		a, err := parseJSONPath(c.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseJSONPath(c.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := pathsOverlap(a, b); got != c.overlap {
			t.Errorf("pathsOverlap(%s, %s) = %v, want %v", c.a, c.b, got, c.overlap)
		}
	}
}

// docsCatalog is a table with JSON columns.
func docsCatalog() Catalog {
	return Catalog{Databases: map[string][]CatalogTable{
		"shop": {{
			Name: "docs",
			Columns: []CatalogColumn{
				col("id", "INT", "int", "int", "", false),
				col("doc", "JSON", "json", "json", "", true),
				col("req", "JSON", "json", "json", "", false),
				col("name", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", true),
			},
			OrderBy: []string{"id"},
			Indexes: []CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
		}},
	}}
}

func testSalt(t *testing.T) *Salt {
	t.Helper()
	salt, err := LoadSalt("", func(string) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	return &salt
}

// pathRules writes the rules of a JSON column: each pair is a path and the
// lines of its rule.
func pathRules(column string, pairs ...string) string {
	var sb strings.Builder
	sb.WriteString("version: 1\nrules:\n  shop.docs." + column + ":\n    paths:\n")
	for i := 0; i < len(pairs); i += 2 {
		sb.WriteString("      " + pairs[i] + ":\n")
		for line := range strings.SplitSeq(pairs[i+1], "\n") {
			sb.WriteString("        " + line + "\n")
		}
	}
	return sb.String()
}

// maskDoc applies the rules to the doc column of one row and returns what the
// column holds afterwards.
func maskDoc(t *testing.T, rules, column, doc string) (string, error) {
	t.Helper()
	plan, err := resolveRules(t, docsCatalog(), rules, testSalt(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return runDoc(t, plan, column, sqlvalue.NewValue("JSON", []byte(doc), false))
}

func runDoc(t *testing.T, plan *Plan, column string, v sqlvalue.Value) (string, error) {
	t.Helper()
	cols := []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: "doc", TypeName: "JSON"}, {Name: "req", TypeName: "JSON"}, {Name: "name", TypeName: "VARCHAR"}}
	fn, err := plan.Table("shop", "docs", cols)
	if err != nil {
		t.Fatal(err)
	}
	row := []sqlvalue.Value{
		sqlvalue.NewValue("INT", []byte("1"), false),
		sqlvalue.NewValue("JSON", []byte("{}"), false),
		sqlvalue.NewValue("JSON", []byte("{}"), false),
		sqlvalue.NewValue("VARCHAR", []byte("n"), false),
	}
	idx := map[string]int{"doc": 1, "req": 2}[column]
	row[idx] = v
	if err := fn(row); err != nil {
		return "", err
	}
	if row[idx].IsNull() {
		return "<NULL>", nil
	}
	return string(row[idx].Bytes()), nil
}

const shapeRule = "use: hash_keep_shape"

func TestPathsKeepEveryOtherByte(t *testing.T) {
	doc := "{ \"z\" :  [1,\t2.50 , 12345678901234567890.123456789,1E+3, \"\\u00e9\\/\\n\"] ,\n" +
		"  \"profile\":{\"email\" :\"alice@example.com\",\"x\":\"a\\\"}]b\"}, \"a\":null }"
	got, err := maskDoc(t, pathRules("doc", "$.profile.email", shapeRule), "doc", doc)
	if err != nil {
		t.Fatal(err)
	}
	const old = `"alice@example.com"`
	before, after, _ := strings.Cut(doc, old)
	prefix, suffix := before, after
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix) {
		t.Fatalf("bytes outside the leaf changed:\n got %q\nwant prefix %q and suffix %q", got, prefix, suffix)
	}
	leaf := got[len(prefix) : len(got)-len(suffix)]
	if len(leaf) != len(old) || leaf == old || leaf[0] != '"' || leaf[len(leaf)-1] != '"' {
		t.Errorf("leaf = %q, want another string as long as %q", leaf, old)
	}
	if leaf[len(`"alice`)] != '@' || leaf[len(`"alice@example`)] != '.' {
		t.Errorf("punctuation changed: %q", leaf)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("not valid JSON: %q", got)
	}
	again, err := maskDoc(t, pathRules("doc", "$.profile.email", shapeRule), "doc", doc)
	if err != nil || again != got {
		t.Errorf("second run = %q, %v; want the same %q", again, err, got)
	}
}

func TestPathsSameValueSameOutputAcrossPaths(t *testing.T) {
	rules := pathRules("doc", "$.a", shapeRule, "$.b[0]", shapeRule, "$.c", shapeRule)
	got, err := maskDoc(t, rules, "doc", `{"a":"hello world","b":["hello world"],"c":"other value"}`)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		A string
		B []string
		C string
	}
	if err := json.Unmarshal([]byte(strings.NewReplacer(`"a"`, `"A"`, `"b"`, `"B"`, `"c"`, `"C"`).Replace(got)), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != v.B[0] || v.A == "hello world" || v.A == v.C {
		t.Errorf("a=%q b=%q c=%q", v.A, v.B[0], v.C)
	}
}

func TestPathsWildcardIndexAndQuotedKey(t *testing.T) {
	rules := pathRules("doc",
		"$.phones[*]", "use: fixed\nvalue: X",
		"$.best[1]", "use: fixed\nvalue: Y",
		`$["a b"]`, "use: fixed\nvalue: Z",
	)
	got, err := maskDoc(t, rules, "doc", `{"phones":["1","2"],"best":["p","q","r"],"a b":"w","keep":"k"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"phones":["X","X"],"best":["p","Y","r"],"a b":"Z","keep":"k"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestPathsAbsentItemsPassThrough(t *testing.T) {
	rules := pathRules("doc", "$.a.b", shapeRule, "$.list[5]", shapeRule, "$.e[*]", shapeRule)
	for _, doc := range []string{
		`{}`,
		`{"a":{}}`,
		`{"a":{"c":"x"}}`,
		`{"list":["a"]}`,
		`{"e":[]}`,
		`{ "other" : "v" }`,
	} {
		got, err := maskDoc(t, rules, "doc", doc)
		if err != nil {
			t.Errorf("%s: %v", doc, err)
		}
		if got != doc {
			t.Errorf("%s changed to %s", doc, got)
		}
	}
}

func TestPathsNullColumnStaysNull(t *testing.T) {
	plan, err := resolveRules(t, docsCatalog(), pathRules("doc", "$.a", shapeRule), testSalt(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := runDoc(t, plan, "doc", sqlvalue.NewValue("JSON", nil, true))
	if err != nil || got != "<NULL>" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestPathsLeafTypes(t *testing.T) {
	cases := []struct {
		name, rule, doc, want string
	}{
		{"fixed string", "use: fixed\nvalue: hi", `{"a":"x"}`, `{"a":"hi"}`},
		{"fixed number on integer", "use: fixed\nvalue: 7", `{"a":123}`, `{"a":7}`},
		{"fixed decimal on float", "use: fixed\nvalue: 1.50", `{"a":-2.5e3}`, `{"a":1.50}`},
		{"fixed bool", "use: fixed\nvalue: true", `{"a":false}`, `{"a":true}`},
		{"fixed keeps null", "use: fixed\nvalue: 1", `{"a":null}`, `{"a":null}`},
		{"hash keeps null", shapeRule, `{"a":null}`, `{"a":null}`},
		{"nullify string", "use: nullify", `{"a":"x"}`, `{"a":null}`},
		{"nullify number", "use: nullify", `{"a": 12 }`, `{"a": null }`},
		{"nullify object", "use: nullify", `{"a":{"b":[1,{"c":"]}"}]},"d":1}`, `{"a":null,"d":1}`},
		{"nullify array", "use: nullify", `{"a":[1,2] }`, `{"a":null }`},
		{"nullify null", "use: nullify", `{"a":null}`, `{"a":null}`},
		{"keep", "use: keep", `{"a" : "x"}`, `{"a" : "x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := maskDoc(t, pathRules("doc", "$.a", c.rule), "doc", c.doc)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestPathsFixedStringEscapes(t *testing.T) {
	// The rule file holds the characters; the output escapes only what JSON must.
	rules := "version: 1\nrules:\n  shop.docs.doc:\n    paths:\n      $.a:\n        use: fixed\n        value: \"q\\\" b\\\\ s/ \\u0001 \\n é 日\"\n"
	got, err := maskDoc(t, rules, "doc", `{"a":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":\"q\\\" b\\\\ s/ \\u0001 \\u000a é 日\"}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("not valid JSON")
	}
}

func TestPathsNumbersKeepEveryDigit(t *testing.T) {
	doc := `{"big":12345678901234567890.123456789,"a":"x","n":[-0.0,1E400]}`
	got, err := maskDoc(t, pathRules("doc", "$.a", "use: fixed\nvalue: y"), "doc", doc)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.Replace(doc, `"a":"x"`, `"a":"y"`, 1) {
		t.Errorf("got %s", got)
	}
}

func TestPathsTypeMismatchIsAnErrorWithoutTheValue(t *testing.T) {
	const secret = "SECRET-VALUE"
	cases := []struct {
		name, rules, doc, want string
	}{
		{"key on array", pathRules("doc", "$.a", shapeRule), `["` + secret + `"]`, "is not an object"},
		{"key on scalar", pathRules("doc", "$.a.b", shapeRule), `{"a":"` + secret + `"}`, "is not an object"},
		{"key on null", pathRules("doc", "$.a.b", shapeRule), `{"a":null}`, "is not an object"},
		{"wildcard on object", pathRules("doc", "$.a[*]", shapeRule), `{"a":{"k":"` + secret + `"}}`, "is not an array"},
		{"index on string", pathRules("doc", "$.a[0]", shapeRule), `{"a":"` + secret + `"}`, "is not an array"},
		{"shape on number", pathRules("doc", "$.a", shapeRule), `{"a":12345}`, "string"},
		{"shape on bool", pathRules("doc", "$.a", shapeRule), `{"a":true}`, "string"},
		{"shape on object", pathRules("doc", "$.a", shapeRule), `{"a":{"k":"` + secret + `"}}`, "string"},
		{"fixed string on number", pathRules("doc", "$.a", "use: fixed\nvalue: x"), `{"a":12345}`, "does not fit"},
		{"fixed number on string", pathRules("doc", "$.a", "use: fixed\nvalue: 5"), `{"a":"` + secret + `"}`, "does not fit"},
		{"fixed on object", pathRules("doc", "$.a", "use: fixed\nvalue: 5"), `{"a":{"k":"` + secret + `"}}`, "does not fit"},
		{"invalid JSON", pathRules("doc", "$.a", shapeRule), `{"a":"` + secret + `",}`, "not valid JSON"},
		{"truncated", pathRules("doc", "$.a", shapeRule), `{"a":"` + secret, "not valid JSON"},
		{"bad literal", pathRules("doc", "$.a", shapeRule), `{"z":tru,"a":"` + secret + `"}`, "not valid JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := maskDoc(t, c.rules, "doc", c.doc)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			for _, leak := range []string{secret, "12345"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error carries a value: %v", err)
				}
			}
			if !strings.Contains(err.Error(), "shop.docs.doc") {
				t.Errorf("the error does not name the column: %v", err)
			}
		})
	}
}

func TestPathsErrorNamesThePath(t *testing.T) {
	_, err := maskDoc(t, pathRules("doc", "$.a.b", shapeRule), "doc", `{"a":[1]}`)
	if err == nil || !strings.Contains(err.Error(), `$.a.b`) {
		t.Fatalf("err = %v", err)
	}
}

func TestPathsValidatedBeforeTheDump(t *testing.T) {
	cases := []struct {
		name, rules, want string
		salt              bool
	}{
		{"syntax", pathRules("doc", "$..a", "use: nullify"), "recursive", true},
		{"syntax slice", pathRules("doc", "$.a[1:2]", "use: nullify"), "slice", true},
		{"root only", pathRules("doc", "$", "use: nullify"), "must point inside", true},
		{"overlap", pathRules("doc", "$.a", "use: nullify", "$.a.b", "use: nullify"), "overlap", true},
		{"overlap wildcard", pathRules("doc", "$.p[*]", "use: nullify", "$.p[0]", "use: nullify"), "overlap", true},
		{"not JSON", "version: 1\nrules:\n  shop.docs.name:\n    paths:\n      $.a:\n        use: nullify\n", "JSON column", true},
		{"no salt", pathRules("doc", "$.a", shapeRule), "needs a salt", false},
		{"nested domain", pathRules("doc", "$.a", "use: nullify\ndomain: d"), "domain is not supported", true},
		{"fixed bad number", pathRules("doc", "$.a", "use: fixed\nvalue: .5"), "JSON number", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg := DefaultRegistry()
			r, err := Parse([]byte(c.rules), reg)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			var salt *Salt
			if c.salt {
				salt = testSalt(t)
			}
			_, err = r.Resolve(docsCatalog(), reg, salt)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestPathsReportEveryProblem(t *testing.T) {
	rules := pathRules("doc", "$..a", "use: nullify", "$.b[1:2]", "use: nullify")
	reg := DefaultRegistry()
	r, err := Parse([]byte(rules), reg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Resolve(docsCatalog(), reg, nil)
	if err == nil || !strings.Contains(err.Error(), "recursive") || !strings.Contains(err.Error(), "slice") {
		t.Fatalf("err = %v", err)
	}
}

func TestPathsMaskedDocIsDeterministic(t *testing.T) {
	doc := `{"profile":{"email":"bob@example.org"},"phones":["090-1111-2222","03-9999-0000"]}`
	rules := pathRules("doc", "$.profile.email", shapeRule, "$.phones[*]", shapeRule)
	a, err := maskDoc(t, rules, "doc", doc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := maskDoc(t, rules, "doc", doc)
	if err != nil || a != b {
		t.Fatalf("%q vs %q (%v)", a, b, err)
	}
	if bytes.Equal([]byte(a), []byte(doc)) {
		t.Error("nothing was masked")
	}
	var got struct {
		Profile struct{ Email string }
		Phones  []string
	}
	if err := json.Unmarshal([]byte(strings.Replace(a, `"profile"`, `"Profile"`, 1)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Phones) != 2 || got.Phones[0][3] != '-' || got.Phones[0] == "090-1111-2222" {
		t.Errorf("phones = %v", got.Phones)
	}
}
