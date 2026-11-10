package sqlvalue

import (
	"strings"
	"testing"
)

func TestSQLiteLiteral(t *testing.T) {
	tests := []struct {
		name, typ, raw string
		null           bool
		want           string
	}{
		{"null", "INT", "", true, "NULL"},
		{"int", "INT", "-12", false, "-12"},
		{"unsigned int", "UNSIGNED INT", "4294967295", false, "4294967295"},
		{"signed bigint", "BIGINT", "-9223372036854775808", false, "-9223372036854775808"},
		// Above int64 SQLite would turn a bare literal into a REAL.
		{"unsigned bigint", "UNSIGNED BIGINT", "18446744073709551615", false, "'18446744073709551615'"},
		{"year", "YEAR", "2024", false, "2024"},
		// DECIMAL is kept as text so scale and precision survive.
		{"decimal", "DECIMAL", "1.10", false, "'1.10'"},
		{"double", "DOUBLE", "1.5e+300", false, "1.5e+300"},
		{"bit small", "BIT", "\x15", false, "21"},
		{"bit max int64", "BIT", "\x7f\xff\xff\xff\xff\xff\xff\xff", false, "9223372036854775807"},
		{"bit 64", "BIT", "\xff\xff\xff\xff\xff\xff\xff\xff", false, "'18446744073709551615'"},
		{"blob", "BLOB", "\x00\xffA", false, "X'00FF41'"},
		{"empty blob", "BLOB", "", false, "X''"},
		{"varbinary", "VARBINARY", "ab", false, "X'6162'"},
		{"text", "VARCHAR", "it's", false, "'it''s'"},
		{"backslash is literal", "VARCHAR", `a\b`, false, `'a\b'`},
		{"empty text", "VARCHAR", "", false, "''"},
		{"date", "DATE", "2024-02-29", false, "'2024-02-29'"},
		{"json", "JSON", `{"a": "b"}`, false, `'{"a": "b"}'`},
		// A raw newline would split the record over two lines.
		{"newline", "TEXT", "a\nb", false, "('a'||char(10)||'b')"},
		// Empty '' pieces next to a break are valid SQL and keep the code simple.
		{"crlf", "TEXT", "a\r\nb", false, "('a'||char(13)||''||char(10)||'b')"},
		{"leading and trailing newline", "TEXT", "\na\n", false, "(''||char(10)||'a'||char(10)||'')"},
		{"only newlines", "TEXT", "\n\n", false, "(''||char(10)||''||char(10)||'')"},
		{"newline and quote", "TEXT", "it's\nx", false, "('it''s'||char(10)||'x')"},
		{"nul", "TEXT", "a\x00\nb", false, "CAST(X'61000A62' AS TEXT)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SQLiteLiteral(tt.typ, []byte(tt.raw), tt.null); got != tt.want {
				t.Errorf("SQLiteLiteral(%q, %q) = %s, want %s", tt.typ, tt.raw, got, tt.want)
			}
		})
	}
}

// Past the limit a value is written as hex, so it stays under SQLite's
// expression depth limit.
func TestSQLiteLiteralManyLineBreaks(t *testing.T) {
	few := SQLiteLiteral("TEXT", []byte(strings.Repeat("\n", maxConcatBreaks)), false)
	if !strings.HasPrefix(few, "(''||char(10)") {
		t.Errorf("at the limit = %.40s, want a concatenation", few)
	}
	many := SQLiteLiteral("TEXT", []byte(strings.Repeat("\n", maxConcatBreaks+1)), false)
	if !strings.HasPrefix(many, "CAST(X'0A0A") || !strings.HasSuffix(many, "' AS TEXT)") {
		t.Errorf("over the limit = %.40s, want a hex cast", many)
	}
}

func TestSQLiteLiterals(t *testing.T) {
	row := []Value{NewValue("INT", []byte("1"), false), NewValue("VARCHAR", nil, true), NewValue("VARCHAR", []byte("x"), false)}
	got := SQLiteLiterals(row)
	want := []string{"1", "NULL", "'x'"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d = %s, want %s", i, got[i], want[i])
		}
	}
}
