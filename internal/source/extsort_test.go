package source

import (
	"context"
	"os"
	"reflect"
	"testing"

	"mysqldance/internal/extsort"
	"mysqldance/internal/sqlvalue"
)

// osScratch is a scratch file on disk that counts how many were made and left.
type osScratch struct {
	*os.File
	live *int
}

func (f osScratch) Remove() error {
	if f.File == nil {
		return nil
	}
	err := f.Close()
	if rerr := os.Remove(f.Name()); rerr != nil && !os.IsNotExist(rerr) {
		return rerr
	}
	*f.live--
	return err
}

// TestEachRowNoKeySpillsAndMatchesInMemory reads a table without a key twice,
// once sorted in memory and once with a limit so small that the sort goes
// through scratch files, and requires the same rows in the same order. It
// includes NULL, an empty string, binary bytes and duplicate rows.
func TestEachRowNoKeySpillsAndMatchesInMemory(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE nk (n INT, s VARCHAR(20), b VARBINARY(8), f DOUBLE)",
		"INSERT INTO nk VALUES (2,'b',X'00ff',1.5),(10,'a',NULL,NULL),(2,'b',X'00ff',1.5),(NULL,'',X'',0),"+
			"(7,'it''s',X'0a0d27',-0.25),(10,NULL,X'41',2)",
	)
	// Many more rows, from a self-join, so there are many runs.
	mustExec(t, conn, "INSERT INTO nk SELECT a.n+x.i, CONCAT(a.s,x.i), a.b, a.f FROM nk a, "+
		"(SELECT 0 i UNION SELECT 1 UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5) x")
	s := openSnapshot(t, cfg, db)
	ti := tableByName(t, s, "nk")
	if len(ti.OrderBy) != 0 {
		t.Fatalf("OrderBy = %v, want none", ti.OrderBy)
	}
	read := func() [][]string {
		var got [][]string
		err := s.EachRow(context.Background(), ti, func(row []sqlvalue.Value) error {
			got = append(got, sqlvalue.Literals(row))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := read()
	if len(want) < 30 {
		t.Fatalf("only %d rows", len(want))
	}

	dir := t.TempDir()
	live, made := 0, 0
	cfg.SortMemory = 512
	cfg.SortFile = func() (extsort.File, error) {
		f, err := os.CreateTemp(dir, "sort-*")
		if err != nil {
			return nil, err
		}
		live++
		made++
		return osScratch{f, &live}, nil
	}
	s = openSnapshot(t, cfg, db)
	got := read()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spilled order differs from the in-memory order:\n got %v\nwant %v", got, want)
	}
	if made < 3 {
		t.Errorf("only %d scratch files; the sort did not spill", made)
	}
	if live != 0 {
		t.Errorf("%d scratch files left after EachRow", live)
	}
}
