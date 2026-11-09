package naming

import (
	"reflect"
	"sort"
	"testing"
)

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

func TestDataFileParts(t *testing.T) {
	if got := DataFileParts("a", "t", 1); len(got) != 1 || got[0] != "a.10-data.t.sql" {
		t.Errorf("one part = %q, want the unnumbered name", got)
	}
	got := DataFileParts("a", "t", 3)
	want := []string{"a.10-data.t.0001.sql", "a.10-data.t.0002.sql", "a.10-data.t.0003.sql"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("three parts = %q, want %q", got, want)
	}
	// More than 9999 parts: every name gets the same width, so the
	// lexicographic order stays the numeric order.
	many := DataFileParts("a", "t", 10000)
	if many[0] != "a.10-data.t.00001.sql" || many[9999] != "a.10-data.t.10000.sql" {
		t.Errorf("10000 parts: first %q, last %q", many[0], many[9999])
	}
	if !sort.StringsAreSorted(many) {
		t.Error("10000 part names are not in lexicographic order")
	}
	if !sort.StringsAreSorted(got) {
		t.Error("part names are not in lexicographic order")
	}
	for _, n := range got {
		if !IsOwnedBy(n, "a") {
			t.Errorf("%q is not owned by its database", n)
		}
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
