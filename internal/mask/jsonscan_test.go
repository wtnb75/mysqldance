package mask

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPathsNestedArraysAndEscapedKeys(t *testing.T) {
	rules := pathRules("doc",
		"$.rows[*][0].v", "use: fixed\nvalue: X",
		"$.rows[1][*].w", "use: fixed\nvalue: Y",
		`$["ab"]`, "use: fixed\nvalue: K",
	)
	doc := `{"rows":[[{"v":"a"},{"v":"b"}],[{"w":"c","v":"keep"},{"w":"d"}]],"ab":"old","}":"]"}`
	got, err := maskDoc(t, rules, "doc", doc)
	if err != nil {
		t.Fatal(err)
	}
	// rows[*][0].v reaches rows[0][0].v and rows[1][0].v, not the [1] elements.
	want := `{"rows":[[{"v":"X"},{"v":"b"}],[{"w":"Y","v":"X"},{"w":"Y"}]],"ab":"K","}":"]"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestPathsEscapedLeafIsDecodedBeforeTheTransform(t *testing.T) {
	// "éé" is two characters once decoded, so the output has two.
	got, err := maskDoc(t, pathRules("doc", "$.a", shapeRule), "doc", `{"a":"éé"}`)
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ A string }
	if err := json.Unmarshal([]byte(strings.Replace(got, `"a"`, `"A"`, 1)), &v); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if n := len([]rune(v.A)); n != 2 {
		t.Errorf("a = %q has %d characters, want 2", v.A, n)
	}
}

// Paths that share a prefix but go on as an object and as an array do not
// overlap, so each document that one of them fits is masked.
func TestPathsSharedPrefixOfObjectAndArray(t *testing.T) {
	rules := pathRules("doc", "$.a.x", "use: fixed\nvalue: X", "$.a[0]", "use: fixed\nvalue: Y")
	for doc, want := range map[string]string{
		`{"a":{"x":"s"}}`: `{"a":{"x":"X"}}`,
		`{"a":["s"]}`:     `{"a":["Y"]}`,
	} {
		got, err := maskDoc(t, rules, "doc", doc)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", doc, got, err, want)
		}
	}
	if _, err := maskDoc(t, rules, "doc", `{"a":"s"}`); err == nil {
		t.Error("a string where an object or an array is needed was accepted")
	}
}

// keep records a decision and changes nothing, so it does not check the shape of
// the document either.
func TestPathsKeepDoesNotCheckTheDocument(t *testing.T) {
	rules := pathRules("doc", "$.meta.id", "use: keep")
	for _, doc := range []string{`{"meta":null}`, `{"meta":5}`, `[1]`} {
		got, err := maskDoc(t, rules, "doc", doc)
		if err != nil || got != doc {
			t.Errorf("%s: got %q, %v", doc, got, err)
		}
	}
	// It still takes part in the overlap check.
	reg := DefaultRegistry()
	r, err := Parse([]byte(pathRules("doc", "$.a", "use: keep", "$.a.b", "use: nullify")), reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(docsCatalog(), reg, nil); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("err = %v", err)
	}
}

// A surrogate escape without its pair does not decode to text: decoding would
// write U+FFFD, and two different stored values would give one output.
func TestPathsLoneSurrogateIsAnError(t *testing.T) {
	rules := pathRules("doc", "$.a", shapeRule)
	for _, doc := range []string{`{"a":"\ud800a"}`, `{"a":"x\udfff"}`, `{"a":"\ud83dx"}`, `{"a":"\ude00\ud83d"}`} {
		if _, err := maskDoc(t, rules, "doc", doc); err == nil || !strings.Contains(err.Error(), "surrogate") {
			t.Errorf("%s: err = %v", doc, err)
		}
	}
	got, err := maskDoc(t, rules, "doc", `{"a":"😀"}`)
	if err != nil || got == `{"a":"😀"}` {
		t.Errorf("a pair: got %q, %v", got, err)
	}
}

// Invalid UTF-8 is an error whether or not the string has escapes (decoding an
// escaped string would turn it into U+FFFD without a word).
func TestPathsInvalidUTF8IsAnErrorWithOrWithoutEscapes(t *testing.T) {
	rules := pathRules("doc", "$.a", shapeRule)
	for _, doc := range []string{"{\"a\":\"caf\xff\"}", "{\"a\":\"caf\\u00e9\xff\"}"} {
		if _, err := maskDoc(t, rules, "doc", doc); err == nil {
			t.Errorf("%q: no error", doc)
		}
	}
}

func TestScanNumber(t *testing.T) {
	good := []string{"0", "-0", "7", "-12", "1.5", "0.25", "1e5", "1E+5", "2.5e-3", "12345678901234567890.123456789"}
	for _, s := range good {
		if end, ok := scanNumber([]byte(s), 0); !ok || end != len(s) {
			t.Errorf("scanNumber(%q) = %d, %v", s, end, ok)
		}
	}
	for _, s := range []string{"", "-", "+1", ".5", "1.", "1e", "1e+", "01", "--1", "-.5"} {
		if end, ok := scanNumber([]byte(s), 0); ok && end == len(s) {
			t.Errorf("scanNumber(%q) accepted the whole text", s)
		}
	}
}
