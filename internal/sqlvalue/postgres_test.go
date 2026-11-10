package sqlvalue

import (
	"strings"
	"testing"
)

func TestPostgresLiteral(t *testing.T) {
	cases := []struct {
		name string
		col  PGColumn
		raw  string
		null bool
		want string
	}{
		{"null", PGColumn{Kind: PGText}, "", true, "NULL"},
		{"integer", PGColumn{Kind: PGNumber}, "-42", false, "-42"},
		{"unsigned bigint", PGColumn{Kind: PGNumber}, "18446744073709551615", false, "18446744073709551615"},
		{"decimal", PGColumn{Kind: PGNumber}, "-0.05", false, "-0.05"},
		{"float exponent", PGColumn{Kind: PGNumber}, "3.40282e+38", false, "3.40282e+38"},
		{"bool true", PGColumn{Kind: PGBool}, "1", false, "true"},
		{"bool false", PGColumn{Kind: PGBool}, "0", false, "false"},
		{"bit padded", PGColumn{Kind: PGBit, Bits: 8}, "\x15", false, "B'00010101'"},
		{"bit 1", PGColumn{Kind: PGBit, Bits: 1}, "\x01", false, "B'1'"},
		{"bit 64", PGColumn{Kind: PGBit, Bits: 64}, "\xff\xff\xff\xff\xff\xff\xff\xff", false, "B'" + strings.Repeat("1", 64) + "'"},
		{"bit zero", PGColumn{Kind: PGBit, Bits: 5}, "\x00", false, "B'00000'"},
		{"text", PGColumn{Kind: PGText}, "it's", false, "'it''s'"},
		{"text empty", PGColumn{Kind: PGText}, "", false, "''"},
		{"text backslash", PGColumn{Kind: PGText}, `a\b`, false, `'a\b'`},
		{"text newline", PGColumn{Kind: PGText}, "a\nb\r\nc'd\\e", false, `E'a\nb\r\nc''d\\e'`},
		{"text japanese", PGColumn{Kind: PGText}, "日本語", false, "'日本語'"},
		{"bytea", PGColumn{Kind: PGBytea}, "\x00\xff A", false, `'\x00FF2041'`},
		{"bytea empty", PGColumn{Kind: PGBytea}, "", false, `'\x'`},
		{"date", PGColumn{Kind: PGDate}, "2024-02-29", false, "'2024-02-29'"},
		{"datetime", PGColumn{Kind: PGTimestamp}, "2024-02-29 01:02:03.456", false, "'2024-02-29 01:02:03.456'"},
		{"time", PGColumn{Kind: PGTime}, "838:59:59", false, "'838:59:59'"},
		{"time negative fraction", PGColumn{Kind: PGTime}, "-00:00:01.5", false, "'-00:00:01.5'"},
	}
	for _, c := range cases {
		got, err := PostgresLiteral(c.col, []byte(c.raw), c.null)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// What PostgreSQL cannot hold is an error, never a changed value.
func TestPostgresLiteralRejects(t *testing.T) {
	cases := []struct {
		name string
		col  PGColumn
		raw  string
		want string
	}{
		{"bool 2", PGColumn{Kind: PGBool}, "2", "TINYINT(1)"},
		{"bool minus", PGColumn{Kind: PGBool}, "-1", "TINYINT(1)"},
		{"NUL in text", PGColumn{Kind: PGText}, "a\x00b", "NUL"},
		{"NUL in text with newline", PGColumn{Kind: PGText}, "a\n\x00", "NUL"},
		{"zero date", PGColumn{Kind: PGDate}, "0000-00-00", "date"},
		{"zero month", PGColumn{Kind: PGDate}, "2024-00-10", "date"},
		{"zero day", PGColumn{Kind: PGDate}, "2024-05-00", "date"},
		// MySQL takes year 0; PostgreSQL has no year 0.
		{"year zero date", PGColumn{Kind: PGDate}, "0000-01-01", "date"},
		{"year zero datetime", PGColumn{Kind: PGTimestamp}, "0000-01-01 00:00:00", "date"},
		{"zero datetime", PGColumn{Kind: PGTimestamp}, "0000-00-00 00:00:00", "date"},
		{"partial datetime", PGColumn{Kind: PGTimestamp}, "2024-00-10 01:02:03", "date"},
		{"bad time", PGColumn{Kind: PGTime}, "12:99", "time"},
		{"bit too wide", PGColumn{Kind: PGBit, Bits: 4}, "\xff", "BIT"},
	}
	for _, c := range cases {
		got, err := PostgresLiteral(c.col, []byte(c.raw), false)
		if err == nil {
			t.Errorf("%s: got %s, want an error", c.name, got)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
}

func TestPostgresLiteralLineBreaksStayOnOneLine(t *testing.T) {
	got, err := PostgresLiteral(PGColumn{Kind: PGText}, []byte("a\nb\rc"), false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("literal has a raw line break: %q", got)
	}
}

func TestQuotePGIdent(t *testing.T) {
	if got := QuotePGIdent(`a"b`); got != `"a""b"` {
		t.Errorf("got %s", got)
	}
}
