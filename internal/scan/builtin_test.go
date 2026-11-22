package scan

import (
	"reflect"
	"testing"
)

func piiConfig(t *testing.T) *Config {
	t.Helper()
	return mustParse(t, "[extend]\nuseBuiltin = [\"pii\"]\n")
}

// secrets returns the text of each finding of the rule in value.
func secrets(c *Config, id, value string) []string {
	var out []string
	for _, m := range c.Scan("db.t.c", value) {
		if m.RuleID == id {
			out = append(out, value[m.Start:m.Start+m.Length])
		}
	}
	return out
}

func TestLuhn(t *testing.T) {
	for s, want := range map[string]bool{
		"4111111111111111":    true,
		"4111 1111 1111 1111": true,
		"4111-1111-1111-1111": true,
		"4111111111111112":    false,
		"378282246310005":     true,  // 15 digits
		"6011111111111117":    true,  // 16
		"4222222222222":       true,  // 13
		"4111111111111x11":    false, // not a digit
		"1":                   false,
		"":                    false,
	} {
		if got := luhn(s); got != want {
			t.Errorf("luhn(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestMyNumber(t *testing.T) {
	// Check digits worked out by hand from the rule (see myNumber). For
	// The weights, from the right, are 2 3 4 5 6 7 for the first six digits and
	// 2 3 4 5 6 for the next five. For 12345678901x the sum is 212, 212 mod 11 =
	// 3, so x = 11-3 = 8. A sum of 0 (r = 0) and a sum of 12 (a 6 in the first
	// place from the right, r = 1) both give 0; a sum of 2 (a 1 there, r = 2)
	// gives 9.
	for _, s := range []string{"123456789018", "000000000000", "000000000060", "000000000019", "1234 5678 9018"} {
		if !myNumber(s) {
			t.Errorf("myNumber(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"123456789010", "123456789011", // wrong check digit
		"000000000001", "000000000061", "000000000010",
		"", "12345678901", "1234567890180", "1234-5678-9011", "12345678901a",
	} {
		if myNumber(s) {
			t.Errorf("myNumber(%q) = true, want false", s)
		}
	}
}

func TestValidateKey(t *testing.T) {
	c := mustParse(t, `
[[rules]]
id = "card"
regex = '''(?:^|[^0-9])([0-9]{16})(?:$|[^0-9])'''
secretGroup = 1
validate = "luhn"
`)
	if got := secrets(c, "card", "pay 4111111111111111 now"); !reflect.DeepEqual(got, []string{"4111111111111111"}) {
		t.Errorf("valid = %v", got)
	}
	if got := secrets(c, "card", "pay 4111111111111112 now"); got != nil {
		t.Errorf("a number that fails the check was found: %v", got)
	}
}

func TestValidateKeyErrors(t *testing.T) {
	wantContains(t, parseErr(t, "[[rules]]\nid = \"r1\"\nregex = 'a'\nvalidate = \"nope\"\n"), `"r1"`, `unknown validate "nope"`, "luhn", "my_number")
	wantContains(t, parseErr(t, "[[rules]]\nid = \"r2\"\npath = 'a'\nvalidate = \"luhn\"\n"), `"r2"`, "validate needs a regex")
}

// A validate that is written but empty is a typo, not an absent key.
func TestValidateKeyEmptyNameIsAnError(t *testing.T) {
	wantContains(t, parseErr(t, "[[rules]]\nid = \"r1\"\nregex = 'a'\nvalidate = \"\"\n"), `"r1"`, "unknown validate")
}

// An [extend] that names nothing adds nothing, and the file still needs rules;
// useDefault is refused whatever its value.
func TestExtendThatNamesNothing(t *testing.T) {
	wantContains(t, parseErr(t, "[extend]\nuseBuiltin = []\n"), "no rules")
	if c := mustParse(t, "[extend]\n[[rules]]\nid = 'r'\nregex = 'a'\n"); len(c.rules) != 1 {
		t.Errorf("rules = %d, want 1", len(c.rules))
	}
	wantContains(t, parseErr(t, "[extend]\nuseDefault = false\n[[rules]]\nid = 'r'\nregex = 'a'\n"), "useDefault", "B-204")
}

func TestPIIRulesLoad(t *testing.T) {
	c := piiConfig(t)
	var ids []string
	for _, r := range c.rules {
		ids = append(ids, r.id)
	}
	want := []string{"pii-email", "pii-phone-jp", "pii-phone-intl", "pii-postal-jp", "pii-credit-card", "pii-my-number", "pii-ipv4", "pii-ipv6"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("rules = %v, want %v", ids, want)
	}
}

func TestPIIRules(t *testing.T) {
	// The check digits are worked out by hand (see TestMyNumber).
	const validMyNumber = "123456789018"
	const badMyNumber = "123456789012"
	spacedMyNumber := validMyNumber[:4] + " " + validMyNumber[4:8] + " " + validMyNumber[8:]

	cases := []struct {
		rule string
		yes  map[string]string // value -> the one secret it holds
		no   []string
	}{
		{"pii-email",
			map[string]string{
				"mail me at taro.yamada+x@example.co.jp please": "taro.yamada+x@example.co.jp",
				"a@b.io":           "a@b.io",
				"x@example.com.":   "x@example.com",
				"日本語 user@d-1.org": "user@d-1.org",
			},
			[]string{"user@localhost", "@handle", "a@b", "no at sign", "user@.com"}},
		{"pii-phone-jp",
			map[string]string{
				"03-1234-5678":       "03-1234-5678",
				"tel 045-123-4567.":  "045-123-4567",
				"090-1234-5678":      "090-1234-5678",
				"(0120-123-456)":     "0120-123-456",
				"0123-45-6789":       "0123-45-6789",
				"01234-5-6789":       "01234-5-6789",
				"a 06-6123-4567 b":   "06-6123-4567",
				"fax:03-1234-5678;;": "03-1234-5678",
			},
			[]string{"0312345678", "09012345678", "03-12345-678", "1-03-1234-5678", "2020-01-2345", "103-1234-5678", "03-1234-56789", "13-1234-5678"}},
		{"pii-phone-intl",
			map[string]string{
				"+81 90-1234-5678":   "+81 90-1234-5678",
				"call +14155552671!": "+14155552671",
				"+442071838750":      "+442071838750",
			},
			[]string{"+1", "+123456", "+0123456789", "81 90 1234 5678", "A+14155552671", "1+14155552671"}},
		{"pii-postal-jp",
			map[string]string{
				"〒100-0001":     "100-0001",
				"zip 150-0002.": "150-0002",
				"100-0001":      "100-0001",
			},
			[]string{"1000001", "100-00011", "2020-1234", "03-123-4567", "10-0001", "1100-0001"}},
		{"pii-credit-card",
			map[string]string{
				"card 4111111111111111 ok":   "4111111111111111",
				"4111 1111 1111 1111":        "4111 1111 1111 1111",
				"4111-1111-1111-1111":        "4111-1111-1111-1111",
				"378282246310005":            "378282246310005",
				"num:4222222222222;":         "4222222222222",
				"4012888888881881 expires 1": "4012888888881881",
			},
			[]string{"4111111111111112", "411111111111", "41111111111111111111", "x411111111111111111111", "1234567890123456"}},
		{"pii-my-number",
			map[string]string{
				"id " + validMyNumber + " end": validMyNumber,
				spacedMyNumber:                 spacedMyNumber,
			},
			[]string{badMyNumber, validMyNumber + "0", "0" + validMyNumber, validMyNumber[:11], "1234-5678-9012"}},
		{"pii-ipv4",
			map[string]string{
				"from 192.168.0.1 to":  "192.168.0.1",
				"0.0.0.0":              "0.0.0.0",
				"255.255.255.255":      "255.255.255.255",
				"ip=10.1.2.3.":         "10.1.2.3",
				"(172.16.254.1)":       "172.16.254.1",
				"a 8.8.8.8, 1.1.1.1 b": "8.8.8.8",
			},
			[]string{"256.1.1.1", "1.2.3", "1.2.3.4.5", "10.1.2.3.4", "01.02.03.04", "1.2.3.999"}},
		{"pii-ipv6",
			map[string]string{
				"2001:0db8:85a3:0000:0000:8a2e:0370:7334": "2001:0db8:85a3:0000:0000:8a2e:0370:7334",
				"addr 2001:db8::8a2e:370:7334 ok":         "2001:db8::8a2e:370:7334",
				"fe80::1":                                 "fe80::1",
				"::1":                                     "::1",
				"2001:db8::":                              "2001:db8::",
				"[2001:DB8::FF00:42:8329]":                "2001:DB8::FF00:42:8329",
			},
			[]string{"12:34:56", "at 12:34", "1:2:3:4:5:6:7", "gggg::1", "::", "2001:db8:::1"}},
	}
	c := piiConfig(t)
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			for value, want := range tc.yes {
				got := secrets(c, tc.rule, value)
				if len(got) == 0 || got[0] != want {
					t.Errorf("%q: found %q, want %q first", value, got, want)
				}
			}
			for _, value := range tc.no {
				if got := secrets(c, tc.rule, value); got != nil {
					t.Errorf("%q: found %q, want nothing", value, got)
				}
			}
		})
	}
}

// The secret of a finding is the number alone, not the characters around it
// that bound it.
func TestPIIFindingIsTheSecretOnly(t *testing.T) {
	c := piiConfig(t)
	v := "pay 4111111111111111 now"
	for _, m := range c.Scan("db.t.c", v) {
		if m.RuleID == "pii-credit-card" && (m.Start != 4 || m.Length != 16) {
			t.Errorf("finding = %+v", m)
		}
	}
}

func TestExtendUseBuiltinAndDisabledRules(t *testing.T) {
	c := mustParse(t, "[extend]\nuseBuiltin = [\"pii\"]\ndisabledRules = [\"pii-ipv4\", \"pii-ipv6\"]\n")
	if got := secrets(c, "pii-ipv4", "10.0.0.1"); got != nil {
		t.Errorf("a disabled rule found %v", got)
	}
	if got := secrets(c, "pii-email", "a@b.io"); len(got) != 1 {
		t.Errorf("email = %v", got)
	}
	if len(c.rules) != 6 {
		t.Errorf("rules = %d, want 6", len(c.rules))
	}
}

func TestExtendOwnRuleReplacesBuiltinOfTheSameID(t *testing.T) {
	c := mustParse(t, `
[extend]
useBuiltin = ["pii"]
[[rules]]
id = "pii-email"
regex = '''[a-z]+@corp\.example'''
[[rules]]
id = "own"
regex = '''zzz'''
`)
	if got := secrets(c, "pii-email", "a@b.io x@corp.example"); !reflect.DeepEqual(got, []string{"x@corp.example"}) {
		t.Errorf("the own rule did not take over: %v", got)
	}
	n := 0
	for _, r := range c.rules {
		if r.id == "pii-email" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d rules with the id pii-email, want 1", n)
	}
	// The built-in rules come first, then the file's own, in file order.
	if last := c.rules[len(c.rules)-1].id; last != "own" || c.rules[len(c.rules)-2].id != "pii-email" {
		t.Errorf("order: ... %s", last)
	}
}

func TestExtendAloneIsAConfig(t *testing.T) {
	c := mustParse(t, "[extend]\nuseBuiltin = [\"pii\"]\n")
	if len(c.rules) == 0 {
		t.Fatal("no rules")
	}
	// Listing a set twice is the same as once.
	if d := mustParse(t, "[extend]\nuseBuiltin = [\"pii\", \"pii\"]\n"); len(d.rules) != len(c.rules) {
		t.Errorf("rules = %d, want %d", len(d.rules), len(c.rules))
	}
}

func TestExtendErrors(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []string
	}{
		"unknown set":         {"[extend]\nuseBuiltin = [\"piii\"]\n[[rules]]\nid='r'\nregex='a'\n", []string{`"piii"`, "pii"}},
		"unknown disabled id": {"[extend]\nuseBuiltin = [\"pii\"]\ndisabledRules = [\"pii-ipv5\"]\n", []string{`"pii-ipv5"`, "disabledRules"}},
		"useDefault":          {"[extend]\nuseDefault = true\n[[rules]]\nid='r'\nregex='a'\n", []string{"useDefault", "B-204"}},
		"path":                {"[extend]\npath = \"other.toml\"\n[[rules]]\nid='r'\nregex='a'\n", []string{"path", "B-204"}},
		"unknown key":         {"[extend]\nuseBuiltins = [\"pii\"]\n[[rules]]\nid='r'\nregex='a'\n", []string{"useBuiltins"}},
		"everything disabled": {"[extend]\nuseBuiltin = [\"pii\"]\ndisabledRules = [\"pii-email\",\"pii-phone-jp\",\"pii-phone-intl\",\"pii-postal-jp\",\"pii-credit-card\",\"pii-my-number\",\"pii-ipv4\",\"pii-ipv6\"]\n", []string{"no rules"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wantContains(t, parseErr(t, tc.src), tc.want...)
		})
	}
}

// A disabled id may also be one of the file's own rules.
func TestDisabledRulesCanNameOwnRule(t *testing.T) {
	c := mustParse(t, "[extend]\nuseBuiltin = [\"pii\"]\ndisabledRules = [\"own\"]\n[[rules]]\nid = \"own\"\nregex = 'zzz'\n")
	if got := secrets(c, "own", "zzz"); got != nil {
		t.Errorf("a disabled own rule found %v", got)
	}
}

// What a scan reports is the rule and the place, never the text, and the same
// value gives the same findings every time.
func TestPIIScanIsDeterministicAndHoldsNoText(t *testing.T) {
	c := piiConfig(t)
	v := "a@b.io; 03-1234-5678; 4111111111111111; 10.0.0.1; fe80::1; 100-0001; 123456789018"
	first := c.Scan("db.t.c", v)
	if len(first) < 6 {
		t.Fatalf("findings = %+v", first)
	}
	for range 5 {
		if got := c.Scan("db.t.c", v); !reflect.DeepEqual(got, first) {
			t.Fatalf("findings changed: %+v vs %+v", got, first)
		}
	}
	if reflect.TypeFor[Match]().NumField() != 3 {
		t.Error("Match carries more than the rule and the place")
	}
}
