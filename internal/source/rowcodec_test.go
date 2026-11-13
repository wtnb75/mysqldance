package source

import (
	"slices"
	"testing"

	"mysqldance/internal/sqlvalue"
)

func TestRowCodecRoundTrip(t *testing.T) {
	cols := []sqlvalue.Column{{TypeName: "int"}, {TypeName: "varchar"}, {TypeName: "blob"}, {TypeName: "varchar"}}
	row := []sqlvalue.Value{
		sqlvalue.NewValue("int", []byte("42"), false),
		sqlvalue.NewValue("varchar", nil, true),                 // NULL
		sqlvalue.NewValue("blob", []byte("\x00\xff\n'"), false), // binary
		sqlvalue.NewValue("varchar", []byte{}, false),           // empty string, not NULL
	}
	got, err := decodeRow(cols, encodeRow(nil, row))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(row) {
		t.Fatalf("got %d values", len(got))
	}
	for i := range row {
		if got[i].IsNull() != row[i].IsNull() || !slices.Equal(got[i].Bytes(), row[i].Bytes()) || got[i].Type() != row[i].Type() {
			t.Errorf("column %d: got %+v, want %+v", i, got[i], row[i])
		}
	}
}

func TestRowCodecRejectsTruncated(t *testing.T) {
	cols := []sqlvalue.Column{{TypeName: "varchar"}, {TypeName: "varchar"}}
	row := []sqlvalue.Value{
		sqlvalue.NewValue("varchar", []byte("hello"), false),
		sqlvalue.NewValue("varchar", []byte("world"), false),
	}
	enc := encodeRow(nil, row)
	for n := range len(enc) {
		if _, err := decodeRow(cols, enc[:n]); err == nil {
			t.Errorf("truncated to %d bytes: no error", n)
		}
	}
	if _, err := decodeRow(cols, append(slices.Clone(enc), 0)); err == nil {
		t.Errorf("trailing byte: no error")
	}
}

func TestSortKeyMatchesJoinedLiterals(t *testing.T) {
	row := []sqlvalue.Value{
		sqlvalue.NewValue("int", []byte("2"), false),
		sqlvalue.NewValue("varchar", []byte("b"), false),
		sqlvalue.NewValue("varchar", nil, true),
	}
	want := "2,'b',NULL"
	if got := string(appendSortKey(nil, row)); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
