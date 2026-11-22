package source

import (
	"context"
	"reflect"
	"testing"

	"mysqldance/internal/mask"
)

// On a server: NULLs fall in one group, 'A' and 'a' are two groups although the
// column's collation says they are equal, and an empty table gives no group.
func TestGroupSizesOnAServer(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE t (z CHAR(2) NOT NULL, n INT NULL, e ENUM('x','y') NULL) DEFAULT CHARSET=utf8mb4",
		"INSERT INTO t VALUES ('A',1,NULL),('a',1,NULL),('A',1,NULL),('b',NULL,'x'),('b',NULL,'x'),('c',NULL,'y')",
		"CREATE TABLE empty_t (z CHAR(2))",
	)
	s := openSnapshot(t, cfg, db)
	ctx := context.Background()
	got, err := s.GroupSizes(ctx, "t", []mask.QuasiColumn{{Name: "z", DataType: "char"}, {Name: "n", DataType: "int"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []GroupSize{{Size: 1, Groups: 2}, {Size: 2, Groups: 2}}; !reflect.DeepEqual(got, want) {
		t.Errorf("z, n: got %v, want %v", got, want)
	}
	// NULL is a value of the ENUM column too: three NULLs, two 'x', one 'y'.
	got, err = s.GroupSizes(ctx, "t", []mask.QuasiColumn{{Name: "e", DataType: "enum"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []GroupSize{{Size: 1, Groups: 1}, {Size: 2, Groups: 1}, {Size: 3, Groups: 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("e: got %v, want %v", got, want)
	}
	got, err = s.GroupSizes(ctx, "empty_t", []mask.QuasiColumn{{Name: "z", DataType: "char"}})
	if err != nil || len(got) != 0 {
		t.Errorf("an empty table: got %v, %v", got, err)
	}
	if _, err := s.GroupSizes(ctx, "t", nil); err == nil {
		t.Error("no column was accepted")
	}
}

func TestGroupSizesQuery(t *testing.T) {
	cols := []mask.QuasiColumn{
		{Name: "zip", DataType: "char"},
		{Name: "born", DataType: "date"},
		{Name: "note", DataType: "TEXT"},
		{Name: "kind", DataType: "enum"},
		{Name: "odd`name", DataType: "int"},
	}
	got := groupSizesQuery("`my``db`.`t`", cols)
	want := "SELECT cnt, COUNT(*) FROM (SELECT COUNT(*) AS cnt FROM `my``db`.`t` " +
		"GROUP BY CAST(`zip` AS BINARY), `born`, CAST(`note` AS BINARY), CAST(`kind` AS BINARY), `odd``name`) g " +
		"GROUP BY cnt ORDER BY cnt"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestComparesAsBinary(t *testing.T) {
	for _, dt := range []string{"char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set", "VARCHAR"} {
		if !comparesAsBinary(dt) {
			t.Errorf("%s must be compared byte by byte", dt)
		}
	}
	for _, dt := range []string{"int", "date", "varbinary", "blob", "json", "decimal", ""} {
		if comparesAsBinary(dt) {
			t.Errorf("%s must be compared as it is", dt)
		}
	}
}
