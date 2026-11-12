package source

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"mysqldance/internal/sqlvalue"
)

// questions returns the session's Questions counter: the number of statements
// the server received on s's connection, including the SHOW that reads it.
func questions(t *testing.T, s *Snapshot) int64 {
	t.Helper()
	var name, value string
	err := s.conn.QueryRowContext(context.Background(), "SHOW SESSION STATUS LIKE 'Questions'").Scan(&name, &value)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// queriesOf returns how many statements fn sends on s's connection. The two
// reads of the counter cost one statement between them, which is subtracted.
func queriesOf(t *testing.T, s *Snapshot, fn func()) int64 {
	t.Helper()
	before := questions(t, s)
	fn()
	return questions(t, s) - before - 1
}

// TestListTablesIsReadOnce checks that the table list is read from the catalog
// once per database: Tables, Views and Defs share it.
func TestListTablesIsReadOnce(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE t1 (id INT PRIMARY KEY, a INT)",
		"CREATE TABLE t2 (id INT PRIMARY KEY, b VARCHAR(5))",
		"CREATE TABLE t3 (id INT PRIMARY KEY, c DATETIME)",
		"CREATE VIEW v1 AS SELECT id FROM t1",
		"CREATE VIEW v2 AS SELECT id FROM v1",
	)
	s := openSnapshot(t, cfg, db)
	ctx := context.Background()

	if n := queriesOf(t, s, func() {}); n != 0 {
		t.Fatalf("the counter itself is off by %d", n)
	}

	// Tables: the list, the columns, the unique keys, and one SHOW CREATE TABLE
	// per table. The type names come from the columns, so no SELECT ... LIMIT 0.
	if n := queriesOf(t, s, func() {
		if _, _, err := s.Tables(ctx); err != nil {
			t.Fatal(err)
		}
	}); n != 3+3 {
		t.Errorf("Tables sent %d statements, want 6 (list, columns, unique keys, 3 SHOW CREATE TABLE)", n)
	}

	// Views: one SHOW CREATE VIEW per view and the dependency query, but not
	// the list again.
	deps := queriesOf(t, s, func() {
		if _, err := s.dialect.viewDeps(ctx, s.conn, s.name, []string{"v1", "v2"}); err != nil {
			t.Fatal(err)
		}
	})
	if n := queriesOf(t, s, func() {
		if _, _, err := s.Views(ctx); err != nil {
			t.Fatal(err)
		}
	}); n != 2+deps {
		t.Errorf("Views sent %d statements, want %d (2 SHOW CREATE VIEW and the dependency query)", n, 2+deps)
	}

	// Defs reads the list from the same cache.
	if n := queriesOf(t, s, func() {
		if _, err := s.listTables(ctx); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Errorf("listTables sent %d statements after Tables, want 0", n)
	}
}

// TestListTablesFollowsUse checks that the cached list is dropped when Use
// switches to another database.
func TestListTablesFollowsUse(t *testing.T) {
	cfg, a, connA := testDB(t)
	_, b, connB := testDB(t)
	mustExec(t, connA, "CREATE TABLE ta (id INT PRIMARY KEY)", "CREATE VIEW va AS SELECT id FROM ta")
	mustExec(t, connB, "CREATE TABLE tb (id INT PRIMARY KEY)", "CREATE VIEW vb AS SELECT id FROM tb")

	ctx := context.Background()
	s := openSnapshot(t, cfg, a)
	if _, _, err := s.Tables(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Use(ctx, b); err != nil {
		t.Fatal(err)
	}
	tables, _, err := s.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Name != "tb" {
		t.Errorf("Tables of %s = %v, want only tb", b, tables)
	}
	views, _, err := s.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Name != "vb" {
		t.Errorf("Views of %s = %v, want only vb", b, views)
	}
}

// TestTableInfoColumnTypes checks the type names Tables reports for a table
// with one column of every type that has a mapping, against the names the
// driver gives to the same columns (what SELECT ... LIMIT 0 returns).
func TestTableInfoColumnTypes(t *testing.T) {
	cfg, db, conn := testDB(t)
	skipOnMySQL56(t, conn, "JSON")
	mustExec(t, conn, "CREATE TABLE ty (id INT PRIMARY KEY,"+
		" c1 TINYINT, c2 SMALLINT, c3 MEDIUMINT, c4 INT, c5 BIGINT,"+
		" u1 TINYINT UNSIGNED, u2 SMALLINT UNSIGNED, u3 MEDIUMINT UNSIGNED, u4 INT UNSIGNED, u5 BIGINT UNSIGNED,"+
		" z1 INT UNSIGNED ZEROFILL, dec_ DECIMAL(10,3), ud DECIMAL(5,2) UNSIGNED, f FLOAT, uf FLOAT UNSIGNED, d DOUBLE,"+
		" bt BIT(5), y YEAR, dt DATETIME, ts TIMESTAMP NULL, da DATE, tm TIME(3),"+
		" ch CHAR(3), vc VARCHAR(20), bn BINARY(3), vb VARBINARY(8),"+
		" tt TINYTEXT, tx TEXT, mt MEDIUMTEXT, lt LONGTEXT, tb TINYBLOB, bl BLOB, mb MEDIUMBLOB, lb LONGBLOB,"+
		" en ENUM('x','y'), st SET('p','q'), js JSON, gm GEOMETRY, pt POINT)")
	s := openSnapshot(t, cfg, db)
	ctx := context.Background()
	ti := tableByName(t, s, "ty")

	names := make([]string, len(ti.Columns))
	for i, c := range ti.Columns {
		names[i] = c.Name
	}
	want, err := s.columnTypes(ctx, "ty", names)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ti.Columns, want) {
		for i := range want {
			if ti.Columns[i] != want[i] {
				t.Errorf("column %s: type name %q, the driver says %q", want[i].Name, ti.Columns[i].TypeName, want[i].TypeName)
			}
		}
	}
}

// TestUnmappedTypeFallsBack checks that a table with a column of a type the
// mapping does not know still gets the driver's type names for all its columns,
// read from the server. The catalog rows are made up: no server type is
// unknown to the mapping on every server.
func TestUnmappedTypeFallsBack(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE u (id INT PRIMARY KEY, g VARCHAR(5), h BIGINT UNSIGNED)")
	s := openSnapshot(t, cfg, db)
	cols := []columnRow{
		{name: "id", dataType: "int", columnType: "int"},
		{name: "g", dataType: "no_such_type", columnType: "no_such_type"},
		{name: "h", dataType: "bigint", columnType: "bigint unsigned"},
	}
	got, err := s.tableColumns(context.Background(), "u", cols)
	if err != nil {
		t.Fatal(err)
	}
	want := []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: "g", TypeName: "VARCHAR"}, {Name: "h", TypeName: "UNSIGNED BIGINT"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tableColumns = %v, want %v", got, want)
	}
	// Generated columns are not selected, so they are not in the result either.
	got, err = s.tableColumns(context.Background(), "u", append(cols[:1:1], columnRow{name: "x", dataType: "int", generated: true}))
	if err != nil || len(got) != 1 || got[0].Name != "id" {
		t.Errorf("tableColumns with a generated column = %v, %v", got, err)
	}
}

func TestCatalogTypeName(t *testing.T) {
	tests := []struct {
		dataType, columnType string
		want                 string
		ok                   bool
	}{
		{"int", "int", "INT", true},
		{"int", "int unsigned", "UNSIGNED INT", true},
		{"int", "int(10) unsigned zerofill", "UNSIGNED INT", true},
		{"tinyint", "tinyint(1)", "TINYINT", true},
		{"bigint", "bigint unsigned", "UNSIGNED BIGINT", true},
		{"smallint", "smallint unsigned", "UNSIGNED SMALLINT", true},
		{"mediumint", "mediumint unsigned", "UNSIGNED MEDIUMINT", true},
		// Only integers carry the prefix in the driver.
		{"decimal", "decimal(5,2) unsigned", "DECIMAL", true},
		{"float", "float unsigned", "FLOAT", true},
		{"double", "double", "DOUBLE", true},
		{"varchar", "varchar(20)", "VARCHAR", true},
		{"VARCHAR", "varchar(20)", "VARCHAR", true},
		{"varbinary", "varbinary(8)", "VARBINARY", true},
		// All four sizes of TEXT and of BLOB are one type to the driver.
		{"tinytext", "tinytext", "TEXT", true},
		{"longtext", "longtext", "TEXT", true},
		{"mediumblob", "mediumblob", "BLOB", true},
		{"point", "point", "GEOMETRY", true},
		{"geometry", "geometry", "GEOMETRY", true},
		// A type the mapping does not know falls back to the driver's name.
		{"uuid", "uuid", "", false},
		{"vector", "vector(3)", "", false},
		{"", "", "", false},
	}
	for _, tc := range tests {
		got, ok := catalogTypeName(tc.dataType, tc.columnType)
		if got != tc.want || ok != tc.ok {
			t.Errorf("catalogTypeName(%q, %q) = %q, %v; want %q, %v", tc.dataType, tc.columnType, got, ok, tc.want, tc.ok)
		}
	}
}
