package naming

import "testing"

func TestEscapeName(t *testing.T) {
	cases := map[string]string{
		"a.b":   "a%2Eb",
		"日":     "%E6%97%A5",
		"100%":  "100%25",
		"a_b-c": "a_b-c",
	}
	for in, want := range cases {
		if got := EscapeName(in); got != want {
			t.Errorf("EscapeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileNames(t *testing.T) {
	if got := SchemaFile("a"); got != "a.00-schema.sql" {
		t.Errorf("SchemaFile = %q", got)
	}
	if got := DataFile("my db", "T"); got != "my%20db.10-data.T.sql" {
		t.Errorf("DataFile = %q", got)
	}
}

func TestIsOwnedBy(t *testing.T) {
	cases := []struct {
		file, db string
		want     bool
	}{
		{"a.10-data.t.sql", "a", true},
		{"a-b.10-data.t.sql", "a", false},
		{"a.00-schema.sql", "a", true},
		{"a.10-data.t.txt", "a", false},
	}
	for _, c := range cases {
		if got := IsOwnedBy(c.file, c.db); got != c.want {
			t.Errorf("IsOwnedBy(%q, %q) = %v, want %v", c.file, c.db, got, c.want)
		}
	}
}

func TestSchemaSortsBeforeData(t *testing.T) {
	for _, db := range []string{"a", "a-b", "a_b", "a.b"} {
		for _, tbl := range []string{"t", "0", "-", "A", "a.b"} {
			if s, d := SchemaFile(db), DataFile(db, tbl); s >= d {
				t.Errorf("SchemaFile(%q)=%q not < DataFile=%q", db, s, d)
			}
		}
	}
}
