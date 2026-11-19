package scan

import (
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *Config {
	t.Helper()
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, src)
	}
	return c
}

func parseErr(t *testing.T, src string) string {
	t.Helper()
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatalf("Parse succeeded, want an error\n%s", src)
	}
	return err.Error()
}

func wantContains(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("error %q does not contain %q", got, s)
		}
	}
}

// standardConfigPath returns the path of the standard gitleaks.toml, which is
// downloaded by `task fetch:gitleaks-config` and not kept in the repository.
// It skips the test when the file has not been downloaded.
func standardConfigPath(t *testing.T) string {
	t.Helper()
	const p = "testdata/gitleaks.toml"
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s is missing; run `task fetch:gitleaks-config`", p)
	}
	return p
}

func TestStandardGitleaksConfigLoads(t *testing.T) {
	c, err := Load(standardConfigPath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := len(c.rules); n < 100 {
		t.Errorf("loaded %d rules, want the full standard set (>100)", n)
	}
	// A rule from the standard set fires on a value it should.
	got := c.Scan("db.t.c", "token AKIAIOSFODNN7ABCDEFG here")
	if len(got) == 0 {
		t.Errorf("the standard rules found nothing in an AWS access key id")
	}
}

func TestDisplayOnlyKeysAreAcceptedAndDropped(t *testing.T) {
	c := mustParse(t, `
title = "my config"
minVersion = "v8.25.0"
[[rules]]
id = "r1"
description = "shown only"
tags = ["a", "b"]
regex = '''abc'''
`)
	if len(c.rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(c.rules))
	}
}

func TestUnsupportedKeysAreErrorsNamingTheKey(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []string
	}{
		"rule key (a CEL expression)": {`
[[rules]]
id = "r1"
regex = 'a'
expr = 'value.contains("a")'
`, []string{"expr"}},
		"top-level key": {`
frobnicate = 1
[[rules]]
id = "r1"
regex = 'a'
`, []string{"frobnicate"}},
		"rule allowlist key": {`
[[rules]]
id = "r1"
regex = 'a'
[[rules.allowlists]]
commits = ["abc"]
`, []string{"commits"}},
		"composite rule": {`
[[rules]]
id = "r1"
regex = 'a'
[[rules.required]]
id = "r2"
`, []string{"required"}},
		"global allowlist key": {`
[allowlist]
targetRules = ["r1"]
regexes = ['x']
[[rules]]
id = "r1"
regex = 'a'
`, []string{"targetRules"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wantContains(t, parseErr(t, tc.src), tc.want...)
		})
	}
}

func TestExtendIsNotSupportedYet(t *testing.T) {
	msg := parseErr(t, `
[extend]
useDefault = true
[[rules]]
id = "r1"
regex = 'a'
`)
	wantContains(t, msg, "extend", "B-204")
}

func TestInvalidRulesAreErrorsNamingTheRule(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []string
	}{
		"bad regex": {`
[[rules]]
id = "bad-re"
regex = '(unclosed'
`, []string{"bad-re", "regex"}},
		"bad path regex": {`
[[rules]]
id = "bad-path"
path = '(unclosed'
`, []string{"bad-path", "path"}},
		"secretGroup too large": {`
[[rules]]
id = "grp"
regex = 'a(b)c'
secretGroup = 2
`, []string{"grp", "secretGroup"}},
		"negative secretGroup": {`
[[rules]]
id = "neg"
regex = 'a(b)c'
secretGroup = -1
`, []string{"neg", "secretGroup"}},
		"no regex and no path": {`
[[rules]]
id = "empty"
keywords = ["x"]
`, []string{"empty", "regex", "path"}},
		"duplicate id": {`
[[rules]]
id = "dup"
regex = 'a'
[[rules]]
id = "dup"
regex = 'b'
`, []string{"dup", "twice"}},
		"missing id": {`
[[rules]]
regex = 'a'
`, []string{"id"}},
		"bad allowlist regex": {`
[[rules]]
id = "al"
regex = 'a'
[[rules.allowlists]]
regexes = ['(unclosed']
`, []string{"al", "allowlist"}},
		"allowlist with no check": {`
[[rules]]
id = "al2"
regex = 'a'
[[rules.allowlists]]
description = "nothing"
`, []string{"al2", "allowlist"}},
		"bad regexTarget": {`
[[rules]]
id = "al3"
regex = 'a'
[[rules.allowlists]]
regexes = ['x']
regexTarget = "bogus"
`, []string{"al3", "regexTarget", "bogus"}},
		"bad condition": {`
[[rules]]
id = "al4"
regex = 'a'
[[rules.allowlists]]
regexes = ['x']
condition = "XOR"
`, []string{"al4", "condition", "XOR"}},
		"negative entropy": {`
[[rules]]
id = "ent"
regex = 'a'
entropy = -1.0
`, []string{"ent", "entropy"}},
		"bad global allowlist regex": {`
[allowlist]
regexes = ['(unclosed']
[[rules]]
id = "r1"
regex = 'a'
`, []string{"allowlist"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wantContains(t, parseErr(t, tc.src), tc.want...)
		})
	}
}

func TestAllProblemsAreReportedTogether(t *testing.T) {
	msg := parseErr(t, `
[[rules]]
id = "one"
regex = '(unclosed'
[[rules]]
id = "two"
keywords = ["x"]
`)
	wantContains(t, msg, "one", "two")
}

func TestEmptyConfigIsAnError(t *testing.T) {
	wantContains(t, parseErr(t, ``), "no rules")
}

func TestLoadReportsThePath(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/bad.toml"
	if err := os.WriteFile(p, []byte("[[rules]]\nid = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load succeeded")
	}
	wantContains(t, err.Error(), p)
}

func TestScanFindsAllMatchesWithPositionAndLength(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "aws"
regex = 'AKIA[0-9A-Z]{16}'
`)
	v := "a=AKIAABCDEFGHIJKLMNOP b=AKIAZZZZZZZZZZZZZZZZ"
	got := c.Scan("db.t.c", v)
	want := []Match{
		{RuleID: "aws", Start: 2, Length: 20},
		{RuleID: "aws", Start: 25, Length: 20},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v", got, want)
	}
	if got := c.Scan("db.t.c", "nothing here"); len(got) != 0 {
		t.Errorf("Scan on a clean value = %+v, want none", got)
	}
	if got := c.Scan("db.t.c", ""); len(got) != 0 {
		t.Errorf("Scan on an empty value = %+v, want none", got)
	}
}

func TestMatchDoesNotCarryTheValue(t *testing.T) {
	// The report must not be able to leak the matched text: Match holds a
	// rule id and two integers only.
	typ := reflect.TypeFor[Match]()
	for f := range typ.Fields() {
		switch f.Name {
		case "RuleID":
			if f.Type.Kind() != reflect.String {
				t.Errorf("RuleID is %v", f.Type)
			}
		default:
			if f.Type.Kind() != reflect.Int {
				t.Errorf("field %s is %v; only RuleID may be a string and the rest must be ints", f.Name, f.Type)
			}
		}
	}
}

func TestScanResultsAreInRuleOrderThenPosition(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "second-in-text"
regex = 'zzz'
[[rules]]
id = "first-in-text"
regex = 'aaa'
`)
	got := c.Scan("db.t.c", "aaa zzz")
	want := []Match{
		{RuleID: "second-in-text", Start: 4, Length: 3},
		{RuleID: "first-in-text", Start: 0, Length: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v (rule order, as in the file)", got, want)
	}
}

func TestKeywordsAreACaseInsensitiveSubstringPrefilter(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "kw"
regex = '(?i)token=[a-z]+'
keywords = ["ToKeN"]
`)
	if got := c.Scan("d.t.c", "x TOKEN=abc"); len(got) != 1 {
		t.Errorf("keyword in other case: %+v, want 1 match", got)
	}
	// The regex alone would match, but the keyword is absent.
	c2 := mustParse(t, `
[[rules]]
id = "kw"
regex = '[a-z]+=abc'
keywords = ["token"]
`)
	if got := c2.Scan("d.t.c", "key=abc"); len(got) != 0 {
		t.Errorf("without the keyword: %+v, want none", got)
	}
	if got := c2.Scan("d.t.c", "mytokenx key=abc"); len(got) != 1 {
		t.Errorf("with the keyword inside a word: %+v, want 1", got)
	}
}

func TestSecretGroup(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "grp"
regex = '(user)=(\w+)'
secretGroup = 2
`)
	got := c.Scan("d.t.c", "x user=alice")
	want := []Match{{RuleID: "grp", Start: 7, Length: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v (the span of group 2)", got, want)
	}
}

func TestSecretGroupThatDidNotMatchFallsBackToTheWholeMatch(t *testing.T) {
	// A finding must not vanish because the chosen group is optional and
	// absent: the whole match is reported instead.
	c := mustParse(t, `
[[rules]]
id = "opt"
regex = 'key=(a)?(\w+)'
secretGroup = 1
`)
	got := c.Scan("d.t.c", "key=xyz")
	want := []Match{{RuleID: "opt", Start: 0, Length: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v", got, want)
	}
}

func TestFirstNonEmptyGroupIsTheSecretWhenSecretGroupIsUnset(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "grp"
regex = 'k(a)?(b+)=1'
`)
	// Group 1 did not participate, so group 2 is the secret.
	got := c.Scan("d.t.c", "kbbb=1")
	want := []Match{{RuleID: "grp", Start: 1, Length: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v", got, want)
	}
	// With no group at all, the whole match is the secret.
	c2 := mustParse(t, `
[[rules]]
id = "nogrp"
regex = 'k=1'
`)
	got = c2.Scan("d.t.c", "xk=1")
	want = []Match{{RuleID: "nogrp", Start: 1, Length: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v", got, want)
	}
}

func TestEmptySecretIsNotAFinding(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "empty"
regex = 'a*'
`)
	if got := c.Scan("d.t.c", "bbb"); len(got) != 0 {
		t.Errorf("zero-length matches reported: %+v", got)
	}
}

func TestShannonEntropyMatchesGitleaks(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"aaaa", 0},
		{"ab", 1},
		{"abcd", 2},
		{"aabb", 1},
	}
	for _, tc := range cases {
		if got := shannonEntropy(tc.in); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("shannonEntropy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	// gitleaks divides by the byte length and counts runes, so a multi-byte
	// rune lowers the result; keep that formula.
	if got := shannonEntropy("é"); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("shannonEntropy(%q) = %v, want 0.5 (one rune over two bytes)", "é", got)
	}
}

func TestEntropyThresholdIsExclusive(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "ent"
regex = '[a-z0-9]{10}'
entropy = 3.0
`)
	if got := c.Scan("d.t.c", "aaaaaaaaaa"); len(got) != 0 {
		t.Errorf("low entropy value reported: %+v", got)
	}
	if got := c.Scan("d.t.c", "a1b2c3d4e5"); len(got) != 1 {
		t.Errorf("high entropy value: %+v, want 1", got)
	}
	// Exactly at the threshold is not a finding (gitleaks skips entropy <= threshold).
	c2 := mustParse(t, `
[[rules]]
id = "ent"
regex = '[ab]{4}'
entropy = 1.0
`)
	if got := c2.Scan("d.t.c", "aabb"); len(got) != 0 {
		t.Errorf("entropy equal to the threshold reported: %+v", got)
	}
}

func TestEntropyIsOfTheSecretGroup(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "ent"
regex = 'k=([a-z0-9]+)'
secretGroup = 1
entropy = 3.0
`)
	if got := c.Scan("d.t.c", "k=aaaaaaaaaa"); len(got) != 0 {
		t.Errorf("low-entropy secret reported: %+v", got)
	}
	if got := c.Scan("d.t.c", "k=a1b2c3d4e5"); len(got) != 1 {
		t.Errorf("high-entropy secret: %+v, want 1", got)
	}
}

func TestPathOnlyRule(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "pw-column"
path = '(?i)\.password$'
`)
	got := c.Scan("shop.users.Password", "hunter2")
	want := []Match{{RuleID: "pw-column", Start: 0, Length: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v (the whole value)", got, want)
	}
	if got := c.Scan("shop.users.name", "hunter2"); len(got) != 0 {
		t.Errorf("a column that does not match the path: %+v", got)
	}
	if got := c.Scan("shop.users.password", ""); len(got) != 0 {
		t.Errorf("an empty value is not a finding: %+v", got)
	}
}

func TestRegexAndPathNeedBothToMatch(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "both"
regex = '\d{4}'
path = '\.cards\.'
`)
	if got := c.Scan("shop.cards.num", "x 1234"); len(got) != 1 {
		t.Errorf("both match: %+v, want 1", got)
	}
	if got := c.Scan("shop.users.num", "x 1234"); len(got) != 0 {
		t.Errorf("path does not match: %+v", got)
	}
	if got := c.Scan("shop.cards.num", "x abcd"); len(got) != 0 {
		t.Errorf("regex does not match: %+v", got)
	}
}

func TestRuleAllowlistRegexesOnTheSecretByDefault(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "r"
regex = 'key=(\w+)'
secretGroup = 1
[[rules.allowlists]]
regexes = ['^EXAMPLE']
`)
	if got := c.Scan("d.t.c", "key=EXAMPLE123"); len(got) != 0 {
		t.Errorf("allowlisted secret reported: %+v", got)
	}
	if got := c.Scan("d.t.c", "key=real123"); len(got) != 1 {
		t.Errorf("other secret: %+v, want 1", got)
	}
}

func TestAllowlistRegexTarget(t *testing.T) {
	src := func(target string) string {
		return `
[[rules]]
id = "r"
regex = 'key=(\w+)'
secretGroup = 1
[[rules.allowlists]]
regexTarget = "` + target + `"
regexes = ['^key=']
`
	}
	// "secret" (the default) sees only the group; "match" sees "key=...";
	// "line" sees the whole value.
	if got := mustParse(t, src("secret")).Scan("d.t.c", "key=abc"); len(got) != 1 {
		t.Errorf("target secret: %+v, want 1 (the secret does not start with key=)", got)
	}
	if got := mustParse(t, src("match")).Scan("d.t.c", "key=abc"); len(got) != 0 {
		t.Errorf("target match: %+v, want none", got)
	}
	if got := mustParse(t, src("line")).Scan("d.t.c", "key=abc"); len(got) != 0 {
		t.Errorf("target line: %+v, want none", got)
	}
	if got := mustParse(t, src("line")).Scan("d.t.c", "x key=abc"); len(got) != 1 {
		t.Errorf("target line with a prefix: %+v, want 1 (^ anchors at the value start)", got)
	}
}

func TestAllowlistStopwords(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "r"
regex = 'key=(\w+)'
secretGroup = 1
[[rules.allowlists]]
stopwords = ["Sample"]
`)
	if got := c.Scan("d.t.c", "key=mySAMPLEvalue"); len(got) != 0 {
		t.Errorf("secret with a stopword (any case): %+v, want none", got)
	}
	if got := c.Scan("d.t.c", "key=other"); len(got) != 1 {
		t.Errorf("secret without a stopword: %+v, want 1", got)
	}
}

func TestAllowlistPaths(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "r"
regex = 'key=\w+'
[[rules.allowlists]]
paths = ['^test\.']
`)
	if got := c.Scan("test.t.c", "key=abc"); len(got) != 0 {
		t.Errorf("allowlisted path: %+v", got)
	}
	if got := c.Scan("prod.t.c", "key=abc"); len(got) != 1 {
		t.Errorf("other path: %+v, want 1", got)
	}
}

func TestAllowlistConditionOrAndAnd(t *testing.T) {
	src := func(cond string) string {
		return `
[[rules]]
id = "r"
regex = 'key=(\w+)'
secretGroup = 1
[[rules.allowlists]]
condition = "` + cond + `"
paths = ['^test\.']
regexes = ['^abc']
`
	}
	or := mustParse(t, src("OR"))
	and := mustParse(t, src("AND"))
	cases := []struct {
		path, value string
		wantOR      int
		wantAND     int
	}{
		{"test.t.c", "key=abc", 0, 0}, // both criteria hold
		{"test.t.c", "key=xyz", 0, 1}, // only the path holds
		{"prod.t.c", "key=abc", 0, 1}, // only the regex holds
		{"prod.t.c", "key=xyz", 1, 1}, // neither holds
	}
	for _, tc := range cases {
		if got := len(or.Scan(tc.path, tc.value)); got != tc.wantOR {
			t.Errorf("OR %s %q: %d findings, want %d", tc.path, tc.value, got, tc.wantOR)
		}
		if got := len(and.Scan(tc.path, tc.value)); got != tc.wantAND {
			t.Errorf("AND %s %q: %d findings, want %d", tc.path, tc.value, got, tc.wantAND)
		}
	}
}

func TestConditionDefaultsToOr(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "r"
regex = 'key=(\w+)'
secretGroup = 1
[[rules.allowlists]]
paths = ['^test\.']
regexes = ['^abc']
`)
	if got := c.Scan("test.t.c", "key=xyz"); len(got) != 0 {
		t.Errorf("default condition is OR, one criterion is enough: %+v", got)
	}
}

func TestGlobalAllowlistsBothForms(t *testing.T) {
	legacy := mustParse(t, `
[allowlist]
paths = ['^skip\.']
stopwords = ["dummy"]
[[rules]]
id = "r"
regex = 'key=(\w+)'
secretGroup = 1
`)
	modern := mustParse(t, `
[[allowlists]]
paths = ['^skip\.']
stopwords = ["dummy"]
[[rules]]
id = "r"
regex = 'key=(\w+)'
secretGroup = 1
`)
	for name, c := range map[string]*Config{"[allowlist]": legacy, "[[allowlists]]": modern} {
		if got := c.Scan("skip.t.c", "key=abc"); len(got) != 0 {
			t.Errorf("%s: allowlisted path: %+v", name, got)
		}
		if got := c.Scan("d.t.c", "key=dummy1"); len(got) != 0 {
			t.Errorf("%s: stopword: %+v", name, got)
		}
		if got := c.Scan("d.t.c", "key=abc"); len(got) != 1 {
			t.Errorf("%s: other value: %+v, want 1", name, got)
		}
	}
}

func TestGlobalAllowlistAppliesToPathOnlyRules(t *testing.T) {
	c := mustParse(t, `
[allowlist]
paths = ['^skip\.']
[[rules]]
id = "r"
path = '\.password$'
`)
	if got := c.Scan("skip.t.password", "x"); len(got) != 0 {
		t.Errorf("globally allowlisted path: %+v", got)
	}
	if got := c.Scan("d.t.password", "x"); len(got) != 1 {
		t.Errorf("other path: %+v, want 1", got)
	}
}

func TestScanIsDeterministic(t *testing.T) {
	c, err := Load(standardConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	v := "AKIAIOSFODNN7ABCDEFG ghp_" + strings.Repeat("aB3dE5", 6) + " -----BEGIN RSA PRIVATE KEY-----"
	first := c.Scan("d.t.c", v)
	for range 20 {
		if got := c.Scan("d.t.c", v); !reflect.DeepEqual(got, first) {
			t.Fatalf("Scan changed between runs: %+v vs %+v", got, first)
		}
	}
}
