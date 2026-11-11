package source

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/sqlvalue"
)

func TestOpenRejectsSystemDatabase(t *testing.T) {
	for _, db := range []string{"mysql", "information_schema", "performance_schema", "sys", "MySQL"} {
		// No server is needed: the name is rejected before connecting.
		_, err := Open(context.Background(), Config{Host: "127.0.0.1", Port: 1}, db)
		if err == nil || !strings.Contains(err.Error(), "system database") {
			t.Errorf("Open(%q) error = %v, want system database error", db, err)
		}
	}
}

func TestOpenRejectsSystemDatabaseOnServer(t *testing.T) {
	cfg, _ := testConfig(t)
	if _, err := Open(context.Background(), cfg, "mysql"); err == nil {
		t.Fatal("Open(mysql) succeeded, want error")
	}
}

func TestCreateDatabase(t *testing.T) {
	cfg, db, _ := testDB(t)
	s := openSnapshot(t, cfg, db)
	got, err := s.CreateDatabase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := "CREATE DATABASE `" + db + "`"; !strings.HasPrefix(got, want) {
		t.Errorf("CreateDatabase = %q, want prefix %q", got, want)
	}
}

func TestRowsPrimaryKeyOrder(t *testing.T) {
	cfg, db, conn := testDB(t)
	// A collation whose order differs from the binary one: as_cs orders a/A
	// next to each other (binary order is A, B, a). MariaDB has no as_cs, so
	// it uses a case-insensitive one (binary order is B, a, c) on other rows.
	// MySQL 5.7 has no utf8mb4_0900 collations and does the same.
	collation, strRows, wantStrs := "utf8mb4_0900_as_cs", "('a'),('B'),('A')", [][]string{{"'A'"}, {"'B'"}, {"'a'"}}
	if isMariaDB(t, conn) || isMySQL57(t, conn) {
		collation, strRows, wantStrs = "utf8mb4_general_ci", "('a'),('B'),('c')", [][]string{{"'B'"}, {"'a'"}, {"'c'"}}
	}
	mustExec(t, conn,
		"CREATE TABLE nums (id INT PRIMARY KEY, v VARCHAR(10))",
		"INSERT INTO nums VALUES (3,'c'),(1,'a'),(10,'j'),(2,'b')",
		"CREATE TABLE strs (k VARCHAR(10) COLLATE "+collation+" PRIMARY KEY)",
		"INSERT INTO strs VALUES "+strRows,
	)
	s := openSnapshot(t, cfg, db)

	nums := tableByName(t, s, "nums")
	if !reflect.DeepEqual(nums.OrderBy, []string{"id"}) || nums.ListColumns {
		t.Errorf("nums: OrderBy=%v ListColumns=%v", nums.OrderBy, nums.ListColumns)
	}
	want := [][]string{{"1", "'a'"}, {"2", "'b'"}, {"3", "'c'"}, {"10", "'j'"}}
	if got := rowsOf(t, s, "nums"); !reflect.DeepEqual(got, want) {
		t.Errorf("nums rows = %v, want %v", got, want)
	}

	if got := rowsOf(t, s, "strs"); !reflect.DeepEqual(got, wantStrs) {
		t.Errorf("strs rows = %v, want %v", got, wantStrs)
	}
}

func TestEachRowMatchesRows(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE keyed (id INT PRIMARY KEY, v VARCHAR(10))",
		"INSERT INTO keyed VALUES (3,'c'),(1,'a'),(10,'j'),(2,'b')",
		"CREATE TABLE nokey (n INT, s VARCHAR(5))",
		"INSERT INTO nokey VALUES (2,'b'),(10,'a'),(2,'b')",
		"CREATE TABLE norows (id INT PRIMARY KEY)",
	)
	s := openSnapshot(t, cfg, db)
	for _, name := range []string{"keyed", "nokey", "norows"} {
		ti := tableByName(t, s, name)
		var got [][]string
		err := s.EachRow(context.Background(), ti, func(row []sqlvalue.Value) error {
			got = append(got, sqlvalue.Literals(row))
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := rowsOf(t, s, name); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Errorf("%s: EachRow = %v, Rows = %v", name, got, want)
		}
	}
}

func TestEachRowStopsOnCallbackError(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE keyed (id INT PRIMARY KEY)",
		"INSERT INTO keyed VALUES (1),(2),(3),(4)",
	)
	s := openSnapshot(t, cfg, db)
	ti := tableByName(t, s, "keyed")
	boom := errors.New("boom")
	calls := 0
	err := s.EachRow(context.Background(), ti, func([]sqlvalue.Value) error {
		calls++
		if calls == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || calls != 2 {
		t.Fatalf("err = %v after %d calls, want boom after 2", err, calls)
	}
	// The snapshot stays usable after the aborted read.
	if got := rowsOf(t, s, "keyed"); len(got) != 4 {
		t.Fatalf("rows after abort = %v", got)
	}
}

func TestOrderByUniqueKeys(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		// Two NOT NULL unique keys: uk_a comes first by index name.
		"CREATE TABLE nn (x INT NULL, b INT NOT NULL, a1 INT NOT NULL, a2 VARCHAR(5) NOT NULL,"+
			" UNIQUE KEY uk_b (b), UNIQUE KEY uk_a (a2, a1))",
		"INSERT INTO nn VALUES (1, 1, 2, 'y'), (2, 2, 1, 'y'), (3, 3, 9, 'x')",
		// Only a nullable unique key: treated as having no key.
		"CREATE TABLE nullable (u INT NULL, v INT, UNIQUE KEY uk (u))",
		"INSERT INTO nullable VALUES (2, 20), (NULL, 0), (1, 10)",
	)
	s := openSnapshot(t, cfg, db)

	nn := tableByName(t, s, "nn")
	if !reflect.DeepEqual(nn.OrderBy, []string{"a2", "a1"}) {
		t.Errorf("nn OrderBy = %v, want [a2 a1]", nn.OrderBy)
	}
	want := [][]string{{"3", "3", "9", "'x'"}, {"2", "2", "1", "'y'"}, {"1", "1", "2", "'y'"}}
	if got := rowsOf(t, s, "nn"); !reflect.DeepEqual(got, want) {
		t.Errorf("nn rows = %v, want %v", got, want)
	}

	nullable := tableByName(t, s, "nullable")
	if len(nullable.OrderBy) != 0 {
		t.Errorf("nullable OrderBy = %v, want empty", nullable.OrderBy)
	}
	// Sorted by the joined literal text in byte order: "1,10" < "2,20" < "NULL,0".
	want = [][]string{{"1", "10"}, {"2", "20"}, {"NULL", "0"}}
	if got := rowsOf(t, s, "nullable"); !reflect.DeepEqual(got, want) {
		t.Errorf("nullable rows = %v, want %v", got, want)
	}
}

func TestRowsNoKeyDuplicates(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE d1 (n INT, s VARCHAR(5))",
		"CREATE TABLE d2 (n INT, s VARCHAR(5))",
		"INSERT INTO d1 VALUES (2,'b'),(10,'a'),(2,'b')",
		"INSERT INTO d2 VALUES (2,'b'),(2,'b'),(10,'a')",
	)
	s := openSnapshot(t, cfg, db)
	// "10,'a'" < "2,'b'" in byte order; both duplicates are kept.
	want := [][]string{{"10", "'a'"}, {"2", "'b'"}, {"2", "'b'"}}
	for _, name := range []string{"d1", "d2"} {
		if ti := tableByName(t, s, name); len(ti.OrderBy) != 0 {
			t.Errorf("%s OrderBy = %v, want empty", name, ti.OrderBy)
		}
		if got := rowsOf(t, s, name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s rows = %v, want %v", name, got, want)
		}
	}
}

func TestGeneratedAndInvisibleColumns(t *testing.T) {
	cfg, db, conn := testDB(t)
	// MySQL 5.7 has no INVISIBLE columns: the table has generated ones only.
	invisible, cols, want := " h INT INVISIBLE,", "id, a, h, c", [][]string{{"1", "5", "7", "'2024-01-02 03:04:05'"}}
	wantNames, vals := []string{"id", "a", "h", "c"}, "1, 5, 7, '2024-01-02 03:04:05'"
	if isMySQL57(t, conn) {
		invisible, cols, vals = "", "id, a, c", "1, 5, '2024-01-02 03:04:05'"
		wantNames, want = []string{"id", "a", "c"}, [][]string{{"1", "5", "'2024-01-02 03:04:05'"}}
	}
	mustExec(t, conn,
		"CREATE TABLE g (id INT PRIMARY KEY, a INT,"+
			" v INT AS (a * 2) VIRTUAL, st INT AS (a + 1) STORED,"+
			invisible+" c DATETIME DEFAULT CURRENT_TIMESTAMP)",
		"INSERT INTO g ("+cols+") VALUES ("+vals+")",
	)
	s := openSnapshot(t, cfg, db)
	g := tableByName(t, s, "g")
	var names []string
	for _, c := range g.Columns {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("Columns = %v, want %v", names, wantNames)
	}
	if !g.ListColumns {
		t.Error("ListColumns = false, want true")
	}
	if got := rowsOf(t, s, "g"); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

func TestEmptyTable(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE e1 (id INT PRIMARY KEY)",
		"CREATE TABLE e2 (n INT)",
	)
	s := openSnapshot(t, cfg, db)
	for _, name := range []string{"e1", "e2"} {
		if got := rowsOf(t, s, name); len(got) != 0 {
			t.Errorf("%s rows = %v, want none", name, got)
		}
	}
}

func TestValueLiterals(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE v (id INT PRIMARY KEY,"+
			" b BLOB, bin BINARY(3), vb VARBINARY(8), bit5 BIT(5),"+
			" dt DATETIME, dt6 DATETIME(6), ts TIMESTAMP NULL,"+
			" j JSON, dec_ DECIMAL(10,3), f FLOAT, txt TEXT, vc VARCHAR(20), ch CHAR(3),"+
			" u BIGINT UNSIGNED, e ENUM('x','y'), d DATE, tm TIME, y YEAR, es VARCHAR(5), eb BLOB)",
		// The fixture session uses +09:00; the snapshot must read UTC.
		"SET time_zone = '+09:00'",
		"INSERT INTO v VALUES (1,"+
			" X'00FF41', 'ab', X'0001', b'10101',"+
			" '2024-01-02 03:04:05', '2024-01-02 03:04:05.123456', '2024-01-02 12:00:00',"+
			` '{"b": 1, "a": [1, "x"]}', -12.5, 1.5, 'line1`+"\n"+`it''s "q"', 'abc', 'xy',`+
			" 18446744073709551615, 'y', '2024-02-29', '-01:02:03', 2024, '', '')",
		"INSERT INTO v (id) VALUES (2)",
	)
	s := openSnapshot(t, cfg, db)
	// The server default may already be UTC, so the ts literal alone cannot
	// prove that Open pins the session time zone; check the session directly.
	var tz string
	if err := s.conn.QueryRowContext(context.Background(), "SELECT @@session.time_zone").Scan(&tz); err != nil {
		t.Fatal(err)
	}
	if tz != "+00:00" {
		t.Errorf("snapshot session time_zone = %q, want +00:00", tz)
	}
	want := [][]string{
		{"1",
			"0x00FF41", "0x616200", "0x0001", "b'10101'",
			"'2024-01-02 03:04:05'", "'2024-01-02 03:04:05.123456'", "'2024-01-02 03:00:00'",
			`'{\"a\": [1, \"x\"], \"b\": 1}'`, "-12.500", "1.5", `'line1\nit\'s \"q\"'`, "'abc'", "'xy'",
			"18446744073709551615", "'y'", "'2024-02-29'", "'-01:02:03'", "2024", "''", "''"},
		{"2", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL",
			"NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL"},
	}
	if isMariaDB(t, conn) {
		// MariaDB's JSON is LONGTEXT: the text is kept as written, where MySQL
		// returns its normalized form (keys sorted).
		want[0][8] = `'{\"b\": 1, \"a\": [1, \"x\"]}'`
	}
	got := rowsOf(t, s, "v")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows =\n%v\nwant\n%v", got, want)
	}
}

func TestViewsAndDeps(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE t (id INT PRIMARY KEY)",
		"CREATE VIEW v1 AS SELECT id FROM t",
		"CREATE VIEW v2 AS SELECT id FROM v1",
	)
	s := openSnapshot(t, cfg, db)

	tables, _, err := s.Tables(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Name != "t" {
		t.Errorf("Tables = %v, want only t", tables)
	}

	views, deps, err := s.Views(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Name != "v1" || views[1].Name != "v2" {
		t.Fatalf("Views = %v, want v1, v2", views)
	}
	for _, v := range views {
		// Names in the dumped database stay unqualified so the dump can be
		// restored under another database name.
		if !strings.HasPrefix(v.Create, "CREATE ") || !strings.Contains(v.Create, "VIEW `"+v.Name+"`") ||
			strings.Contains(v.Create, db) {
			t.Errorf("view %s Create = %q", v.Name, v.Create)
		}
	}
	if want := map[string][]string{"v2": {"v1"}}; !reflect.DeepEqual(deps, want) {
		t.Errorf("deps = %v, want %v", deps, want)
	}
}

func TestCheckAllColumnsVisible(t *testing.T) {
	create := "CREATE TABLE `t` (\n  `id` int NOT NULL,\n  `h` int DEFAULT NULL /*!80023 INVISIBLE */,\n" +
		"  `s` int DEFAULT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB"
	all := []columnRow{{name: "id"}, {name: "h", invisible: true}, {name: "s"}}
	if err := checkAllColumnsVisible(create, all); err != nil {
		t.Errorf("all columns visible: %v", err)
	}
	err := checkAllColumnsVisible(create, []columnRow{{name: "id"}, {name: "s"}})
	if err == nil || !strings.Contains(err.Error(), "`h`") {
		t.Errorf("hidden column: err = %v, want an error naming `h`", err)
	}
	if err := checkAllColumnsVisible("CREATE TABLE `t`", all); err == nil {
		t.Error("unparsable statement: err = nil, want an error")
	}
}

// TestSelectList checks that a FLOAT is read as an exact DOUBLE (B-059) and
// every other column as itself.
func TestSelectList(t *testing.T) {
	cols := []sqlvalue.Column{
		{Name: "id", TypeName: "INT"},
		{Name: "f", TypeName: "FLOAT"},
		{Name: "d", TypeName: "DOUBLE"},
		{Name: "u`x", TypeName: "UNSIGNED FLOAT"},
	}
	got := selectList(cols)
	want := "`id`, `f` * 1e0, `d`, `u``x` * 1e0"
	if got != want {
		t.Errorf("selectList = %q, want %q", got, want)
	}
}

func TestOrderByClause(t *testing.T) {
	got, err := orderByClause([]string{"a", "b", "c"}, []string{"varchar", "int", "text"})
	want := " ORDER BY CAST(`a` AS BINARY), `b`, CAST(`c` AS BINARY)"
	if err != nil || got != want {
		t.Errorf("orderByClause = %q, %v, want %q", got, err, want)
	}
	if got, err := orderByClause(nil, nil); err != nil || got != "" {
		t.Errorf("orderByClause(nil) = %q, %v, want empty", got, err)
	}
}

// TestOrderByClauseRejectsMissingTypes covers a TableInfo whose key types are
// absent: the key would otherwise be sorted by collation (or panic).
func TestOrderByClauseRejectsMissingTypes(t *testing.T) {
	for name, types := range map[string][]string{
		"nil":      nil,
		"short":    {"int"},
		"empty":    {"int", ""},
		"too long": {"int", "int", "int"},
	} {
		if got, err := orderByClause([]string{"a", "b"}, types); err == nil {
			t.Errorf("%s: orderByClause = %q, want an error", name, got)
		}
	}
}

// TestOrderByTypesFromTables checks that Tables records the key column types
// in TableInfo, which Rows uses instead of any state kept in the Snapshot.
func TestOrderByTypesFromTables(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE k (name VARCHAR(10), n INT, PRIMARY KEY (name, n))")
	s := openSnapshot(t, cfg, db)
	ti := tableByName(t, s, "k")
	if want := []string{"varchar", "int"}; !reflect.DeepEqual(ti.OrderByTypes, want) {
		t.Errorf("OrderByTypes = %v, want %v", ti.OrderByTypes, want)
	}
}

func TestWarningsLargeTable(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE keyed (id INT PRIMARY KEY)",
		"CREATE TABLE loose (n INT)",
	)
	// TABLE_ROWS of a new table is usually 0, so lower the threshold below
	// it instead of loading 500k rows.
	orig := largeTableRows
	largeTableRows = -1
	t.Cleanup(func() { largeTableRows = orig })
	s := openSnapshot(t, cfg, db)
	w := warningsOf(t, s)
	// A table with a key is streamed, so its size is no reason to warn.
	if len(w) != 1 {
		t.Fatalf("Warnings = %q, want one, for the table without a key", w)
	}
	if !strings.Contains(w[0], "`loose`") || !strings.Contains(w[0], "in memory") || !strings.Contains(w[0], "sorted") {
		t.Errorf("loose warning = %q", w[0])
	}
}

func TestWarningsNonInnoDB(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE inno (id INT PRIMARY KEY) ENGINE=InnoDB",
		"CREATE TABLE my (id INT PRIMARY KEY) ENGINE=MyISAM",
	)
	cfg.NoLock = true
	s := openSnapshot(t, cfg, db)
	if ti := tableByName(t, s, "my"); ti.Engine != "MyISAM" {
		t.Errorf("Engine = %q, want MyISAM", ti.Engine)
	}
	w := warningsOf(t, s)
	if len(w) != 1 || !strings.Contains(w[0], "`my`") || !strings.Contains(w[0], "MyISAM") {
		t.Errorf("Warnings = %v, want one warning about `my` (MyISAM)", w)
	}
}
