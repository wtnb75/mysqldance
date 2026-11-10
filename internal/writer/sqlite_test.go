package writer

import (
	"bytes"
	"database/sql"
	"io"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// The SQLite writer must be a Writer.
var _ Writer = (*SQLite)(nil)

func TestNeedsDefs(t *testing.T) {
	if NewMySQL(datafile.Options{}).NeedsDefs() {
		t.Error("the MySQL writer does not need table definitions")
	}
	if !NewSQLite(datafile.Options{}).NeedsDefs() {
		t.Error("the SQLite writer builds its DDL from table definitions")
	}
}

// memDB returns an in-memory SQLite database on a single connection, so that
// PRAGMA and BEGIN apply to every later statement.
//
//go:fix inline
func memDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqliteExec(t *testing.T, db *sql.DB, script string) {
	t.Helper()
	if _, err := db.Exec(script); err != nil {
		t.Fatalf("SQLite rejected the script: %v\n%.1000s", err, script)
	}
}

func relationalSchema() Schema {
	parent := source.TableInfo{Name: "parent", Columns: []sqlvalue.Column{
		{Name: "id", TypeName: "UNSIGNED BIGINT"}, {Name: "code", TypeName: "VARCHAR"}}}
	child := source.TableInfo{Name: "child", Columns: []sqlvalue.Column{
		{Name: "id", TypeName: "INT"}, {Name: "parent_id", TypeName: "UNSIGNED BIGINT"},
		{Name: "code", TypeName: "VARCHAR"}, {Name: "qty", TypeName: "DECIMAL"},
		{Name: "created", TypeName: "DATETIME"}, {Name: "note", TypeName: "VARCHAR"}}}
	return Schema{
		DB:     "d",
		Tables: []source.TableInfo{child, parent},
		Defs: map[string]source.TableDef{
			"parent": {
				Columns: []source.ColumnDef{
					{Name: "id", Type: "bigint unsigned", DataType: "bigint"},
					{Name: "code", Type: "varchar(10)", DataType: "varchar"},
				},
				Indexes: []source.IndexDef{
					{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id"}}},
					{Name: "uk_code", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "code"}}},
					{Name: "uk_id_code", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id"}, {Column: "code"}}},
				},
			},
			"child": {
				Columns: []source.ColumnDef{
					{Name: "id", Type: "int", DataType: "int", AutoIncrement: true},
					{Name: "parent_id", Type: "bigint unsigned", DataType: "bigint", Nullable: true},
					{Name: "code", Type: "varchar(10)", DataType: "varchar", Default: new("x")},
					{Name: "qty", Type: "decimal(10,2)", DataType: "decimal", Default: new("1.50")},
					{Name: "created", Type: "datetime", DataType: "datetime", Default: new("CURRENT_TIMESTAMP"), DefaultExpr: true, OnUpdate: true},
					{Name: "note", Type: "varchar(100)", DataType: "varchar", Nullable: true},
					{Name: "twice", Type: "int", DataType: "int", Nullable: true, Generated: true},
				},
				Indexes: []source.IndexDef{
					{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id"}}},
					{Name: "ft_note", Kind: "FULLTEXT", Parts: []source.IndexPart{{Column: "note"}}},
					{Name: "fn", Kind: "BTREE", Parts: []source.IndexPart{{}}},
					{Name: "idx_note", Kind: "BTREE", Parts: []source.IndexPart{{Column: "note", Prefix: true}, {Column: "created", Desc: true}}},
				},
				ForeignKeys: []source.ForeignKey{
					{Name: "fk_parent", Columns: []string{"parent_id", "code"}, RefSchema: "d", RefTable: "parent",
						RefColumns: []string{"id", "code"}, OnUpdate: "RESTRICT", OnDelete: "CASCADE"},
					{Name: "fk_other", Columns: []string{"code"}, RefSchema: "other", RefTable: "x", RefColumns: []string{"c"},
						OnUpdate: "NO ACTION", OnDelete: "NO ACTION"},
				},
				Checks: []string{"ck_qty"},
			},
		},
		Views: []schemafile.View{{Name: "v", Create: "CREATE VIEW `v` AS select 1"}},
	}
}

const wantRelationalDDL = "PRAGMA foreign_keys=OFF;\n" +
	"CREATE TABLE `child` (\n" +
	"  `id` INTEGER NOT NULL,\n" +
	"  `parent_id` TEXT,\n" +
	"  `code` TEXT NOT NULL DEFAULT 'x',\n" +
	"  `qty` TEXT NOT NULL DEFAULT '1.50',\n" +
	"  `created` TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,\n" +
	"  `note` TEXT,\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  FOREIGN KEY (`parent_id`, `code`) REFERENCES `parent` (`id`, `code`) ON DELETE CASCADE ON UPDATE RESTRICT\n" +
	");\n" +
	"CREATE INDEX `child__idx_note` ON `child` (`note`, `created` DESC);\n" +
	"CREATE TABLE `parent` (\n" +
	"  `id` TEXT NOT NULL,\n" +
	"  `code` TEXT NOT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	");\n" +
	"CREATE UNIQUE INDEX `parent__uk_code` ON `parent` (`code`);\n" +
	"CREATE UNIQUE INDEX `parent__uk_id_code` ON `parent` (`id`, `code`);\n"

func TestSQLiteSchema(t *testing.T) {
	var warnings []string
	w := NewSQLite(datafile.Options{})
	var got bytes.Buffer
	schema := relationalSchema()
	schema.Warn = func(msg string) { warnings = append(warnings, msg) }
	if err := w.WriteSchema(&got, schema); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(warnings, "\n")
	if got.String() != wantRelationalDDL {
		t.Errorf("schema =\n%s\nwant\n%s", got.String(), wantRelationalDDL)
	}
	sqliteExec(t, memDB(t), got.String())

	// Whatever is not written is reported, never dropped silently.
	for _, want := range []string{
		"table `child`: generated column `twice` is not output",
		"table `child`: ON UPDATE of column `created` is not output",
		"table `child`: index `ft_note` (FULLTEXT) is not output",
		"table `child`: index `fn` has an expression key part and is not output",
		"table `child`: index `idx_note` covers a column prefix; the whole column is indexed",
		"table `child`: foreign key `fk_other` references another database and is not output",
		"table `child`: CHECK constraint `ck_qty` is not output",
		"view `v` is not output",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("warnings lack %q:\n%s", want, log)
		}
	}
}

func TestSQLiteSchemaDeterministic(t *testing.T) {
	render := func() string {
		var b bytes.Buffer
		if err := NewSQLite(datafile.Options{}).WriteSchema(&b, relationalSchema()); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if a, b := render(), render(); a != b {
		t.Errorf("two renderings differ:\n%s\n%s", a, b)
	}
}

func TestSQLiteSchemaErrors(t *testing.T) {
	cases := map[string]func(s *Schema){
		"no definition": func(s *Schema) { delete(s.Defs, "parent") },
		"columns differ from the data columns": func(s *Schema) {
			d := s.Defs["parent"]
			d.Columns = d.Columns[:1]
			s.Defs["parent"] = d
		},
		"index name collides": func(s *Schema) {
			// parent__uk_code is also the name of a table.
			s.Tables = append(s.Tables, source.TableInfo{Name: "parent__uk_code",
				Columns: []sqlvalue.Column{{Name: "id", TypeName: "INT"}}})
			s.Defs["parent__uk_code"] = source.TableDef{Columns: []source.ColumnDef{{Name: "id", Type: "int", DataType: "int"}}}
		},
		"no output column": func(s *Schema) {
			s.Tables = []source.TableInfo{{Name: "g"}}
			s.Defs = map[string]source.TableDef{"g": {Columns: []source.ColumnDef{{Name: "x", Type: "int", DataType: "int", Generated: true}}}}
		},
		"unknown column type": func(s *Schema) {
			d := s.Defs["parent"]
			d.Columns[1].DataType = "frobnicate"
			s.Defs["parent"] = d
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := relationalSchema()
			mutate(&s)
			var b bytes.Buffer
			if err := NewSQLite(datafile.Options{}).WriteSchema(&b, s); err == nil {
				t.Errorf("WriteSchema succeeded:\n%s", b.String())
			}
		})
	}
}

// sqliteRoundTrip writes the schema and the data of a table through the SQLite
// writer into SQLite and returns the database.
func sqliteRoundTrip(t *testing.T, w *SQLite, schema Schema, rows [][]sqlvalue.Value) *sql.DB {
	t.Helper()
	db := memDB(t)
	var ddl bytes.Buffer
	if err := w.WriteSchema(&ddl, schema); err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, db, ddl.String())
	parts := w.DataParts(schema.DB, schema.Tables[0], rows)
	if len(parts) == 0 {
		t.Fatal("no data part")
	}
	for _, p := range parts {
		var b bytes.Buffer
		if err := p(&b); err != nil {
			t.Fatal(err)
		}
		sqliteExec(t, db, b.String())
	}
	return db
}

func typesSchema() Schema {
	cols := []struct{ name, typ, data, driver string }{
		{"id", "int", "int", "INT"},
		{"name", "varchar(20)", "varchar", "VARCHAR"},
		{"price", "decimal(10,2)", "decimal", "DECIMAL"},
		{"big", "bigint unsigned", "bigint", "UNSIGNED BIGINT"},
		{"flags", "bit(64)", "bit", "BIT"},
		{"small", "bit(8)", "bit", "BIT"},
		{"data", "blob", "blob", "BLOB"},
		{"f", "double", "double", "DOUBLE"},
		{"d", "date", "date", "DATE"},
		{"j", "json", "json", "JSON"},
	}
	info := source.TableInfo{Name: "t", OrderBy: []string{"id"}, OrderByTypes: []string{"int"}}
	def := source.TableDef{Indexes: []source.IndexDef{
		{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id"}}}}}
	for _, c := range cols {
		info.Columns = append(info.Columns, sqlvalue.Column{Name: c.name, TypeName: c.driver})
		def.Columns = append(def.Columns, source.ColumnDef{Name: c.name, Type: c.typ, DataType: c.data, Nullable: c.name != "id"})
	}
	return Schema{DB: "d", Tables: []source.TableInfo{info}, Defs: map[string]source.TableDef{"t": def}}
}

func typedRow(id string, cells ...string) []sqlvalue.Value {
	types := []string{"INT", "VARCHAR", "DECIMAL", "UNSIGNED BIGINT", "BIT", "BIT", "BLOB", "DOUBLE", "DATE", "JSON"}
	row := []sqlvalue.Value{sqlvalue.NewValue("INT", []byte(id), false)}
	for i, c := range cells {
		if c == "<null>" {
			row = append(row, sqlvalue.NewValue(types[i+1], nil, true))
			continue
		}
		row = append(row, sqlvalue.NewValue(types[i+1], []byte(c), false))
	}
	return row
}

func TestSQLiteValuesRoundTrip(t *testing.T) {
	rows := [][]sqlvalue.Value{
		typedRow("1", "it's a\nline\r\nbreak \\ ok", "1.10", "18446744073709551615",
			"\xff\xff\xff\xff\xff\xff\xff\xff", "\x15", "\x00\xffA", "1.5e+300", "2024-02-29", `{"a": 1}`),
		typedRow("2", "", "-0.05", "0", "\x01", "\x00", "", "-2.25", "0000-00-00", "null"),
		typedRow("3", "<null>", "<null>", "<null>", "<null>", "<null>", "<null>", "<null>", "<null>", "<null>"),
		typedRow("4", "a\x00b", "123456789012345678.9", "9223372036854775808",
			"\x7f\xff\xff\xff\xff\xff\xff\xff", "\xff", "ab", "3", "9999-12-31", "[]"),
	}
	db := sqliteRoundTrip(t, NewSQLite(datafile.Options{}), typesSchema(), rows)

	type rec struct {
		id                                int
		name, price, big, flags, small, d sql.NullString
		data                              []byte
		f                                 sql.NullFloat64
		j                                 sql.NullString
	}
	var got []rec
	q, err := db.Query("SELECT id, name, price, big, flags, small, data, f, d, j FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	for q.Next() {
		var r rec
		if err := q.Scan(&r.id, &r.name, &r.price, &r.big, &r.flags, &r.small, &r.data, &r.f, &r.d, &r.j); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := q.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("restored %d rows, want 4", len(got))
	}
	check := func(what string, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", what, got, want)
		}
	}
	check("name 1", got[0].name.String, "it's a\nline\r\nbreak \\ ok")
	check("price 1", got[0].price.String, "1.10")
	check("big 1", got[0].big.String, "18446744073709551615")
	check("flags 1", got[0].flags.String, "18446744073709551615")
	check("small 1", got[0].small.String, "21")
	check("data 1", string(got[0].data), "\x00\xffA")
	if got[0].f.Float64 != 1.5e300 {
		t.Errorf("f 1 = %v", got[0].f.Float64)
	}
	check("d 1", got[0].d.String, "2024-02-29")
	check("j 1", got[0].j.String, `{"a": 1}`)
	if !got[1].name.Valid || got[1].name.String != "" {
		t.Errorf("empty string became %+v", got[1].name)
	}
	check("price 2", got[1].price.String, "-0.05")
	check("d 2", got[1].d.String, "0000-00-00")
	if got[2].name.Valid || got[2].price.Valid || got[2].big.Valid || got[2].flags.Valid || got[2].data != nil || got[2].f.Valid {
		t.Errorf("NULLs not kept: %+v", got[2])
	}
	check("name 4", got[3].name.String, "a\x00b")
	check("price 4", got[3].price.String, "123456789012345678.9")
	check("big 4", got[3].big.String, "9223372036854775808")
	check("flags 4", got[3].flags.String, "9223372036854775807")
}

// Every record sits on its own line, so a diff touches one line per change.
func TestSQLiteOneRecordPerLine(t *testing.T) {
	w := NewSQLite(datafile.Options{})
	rows := [][]sqlvalue.Value{typedRow("1", "a\nb", "1", "1", "\x01", "\x01", "x", "1", "2024-01-01", "1")}
	parts := w.DataParts("d", typesSchema().Tables[0], rows)
	var b bytes.Buffer
	if err := parts[0](&b); err != nil {
		t.Fatal(err)
	}
	want := "PRAGMA foreign_keys=OFF;\nBEGIN;\nINSERT INTO `t` VALUES\n" +
		"(1,('a'||char(10)||'b'),'1','1',1,1,X'78',1,'2024-01-01','1');\nCOMMIT;\n"
	if b.String() != want {
		t.Errorf("data file =\n%s\nwant\n%s", b.String(), want)
	}
}

// A value with very many line breaks must still load: a long chain of || would
// pass SQLite's expression depth limit.
func TestSQLiteManyLineBreaks(t *testing.T) {
	text := strings.Repeat("line\n", 5000) + "end"
	rows := [][]sqlvalue.Value{typedRow("1", text, "1", "1", "\x01", "\x01", "x", "1", "2024-01-01", "1")}
	db := sqliteRoundTrip(t, NewSQLite(datafile.Options{}), typesSchema(), rows)
	var got string
	if err := db.QueryRow("SELECT name FROM t").Scan(&got); err != nil || got != text {
		t.Errorf("restored %d bytes, err %v; want %d bytes", len(got), err, len(text))
	}
}

func TestSQLitePrimaryKeyPrefixWarns(t *testing.T) {
	s := typesSchema()
	d := s.Defs["t"]
	d.Indexes = []source.IndexDef{{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id", Prefix: true}}}}
	s.Defs["t"] = d
	var warnings []string
	s.Warn = func(msg string) { warnings = append(warnings, msg) }
	if err := NewSQLite(datafile.Options{}).WriteSchema(io.Discard, s); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(warnings, "\n"); !strings.Contains(got, "index `PRIMARY` covers a column prefix") {
		t.Errorf("no warning about the primary key prefix:\n%s", got)
	}
}

func TestSQLiteSplitsFilesAndStatements(t *testing.T) {
	var rows [][]sqlvalue.Value
	for i := range 40 {
		rows = append(rows, typedRow(strings.Repeat("1", 1)+string(rune('0'+i/10))+string(rune('0'+i%10)),
			"n", "1", "1", "\x01", "\x01", "x", "1", "2024-01-01", "1"))
	}
	w := NewSQLite(datafile.Options{RowsPerInsert: 3, RowsPerFile: 10})
	schema := typesSchema()
	db := sqliteRoundTrip(t, w, schema, rows)
	parts := w.DataParts("d", schema.Tables[0], rows)
	if len(parts) < 2 {
		t.Fatalf("got %d files, want the table split into several", len(parts))
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 40 {
		t.Errorf("restored %d rows, want 40", n)
	}
}

func TestSQLiteEmptyTableHasOneFile(t *testing.T) {
	w := NewSQLite(datafile.Options{})
	db := sqliteRoundTrip(t, w, typesSchema(), nil)
	var n int
	if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 0 {
		t.Errorf("count = %d, %v", n, err)
	}
}

// Identifiers with quotes and odd characters survive.
func TestSQLiteOddNames(t *testing.T) {
	info := source.TableInfo{Name: "we`ird \"t\"", Columns: []sqlvalue.Column{{Name: "co`l", TypeName: "INT"}}}
	schema := Schema{DB: "d", Tables: []source.TableInfo{info}, Defs: map[string]source.TableDef{
		info.Name: {Columns: []source.ColumnDef{{Name: "co`l", Type: "int", DataType: "int", Nullable: true}},
			Indexes: []source.IndexDef{{Name: "i`x", Kind: "BTREE", Parts: []source.IndexPart{{Column: "co`l"}}}}}}}
	rows := [][]sqlvalue.Value{{sqlvalue.NewValue("INT", []byte("7"), false)}}
	db := sqliteRoundTrip(t, NewSQLite(datafile.Options{}), schema, rows)
	var v int
	if err := db.QueryRow("SELECT `co``l` FROM `we``ird \"t\"`").Scan(&v); err != nil || v != 7 {
		t.Errorf("v = %d, %v", v, err)
	}
}

func TestSQLiteDefaults(t *testing.T) {
	info := source.TableInfo{Name: "t", Columns: []sqlvalue.Column{
		{Name: "a", TypeName: "INT"}, {Name: "b", TypeName: "VARCHAR"}, {Name: "c", TypeName: "VARCHAR"},
		{Name: "d", TypeName: "DOUBLE"}, {Name: "e", TypeName: "DATETIME"}, {Name: "f", TypeName: "BIT"},
		{Name: "g", TypeName: "INT"}}}
	def := source.TableDef{Columns: []source.ColumnDef{
		{Name: "a", Type: "int", DataType: "int", Default: new("-5")},
		{Name: "b", Type: "varchar(5)", DataType: "varchar", Default: new("it's")},
		{Name: "c", Type: "varchar(5)", DataType: "varchar", Default: new("a\nb")},
		{Name: "d", Type: "double", DataType: "double", Default: new("2.5")},
		{Name: "e", Type: "datetime(3)", DataType: "datetime", Default: new("CURRENT_TIMESTAMP(3)"), DefaultExpr: true},
		{Name: "f", Type: "bit(8)", DataType: "bit", Default: new("b'101'")},
		{Name: "g", Type: "int", DataType: "int", Nullable: true, Default: new("(1 + 2)"), DefaultExpr: true},
	}}
	var warnings []string
	w := NewSQLite(datafile.Options{})
	var ddl bytes.Buffer
	if err := w.WriteSchema(&ddl, Schema{DB: "d", Tables: []source.TableInfo{info}, Defs: map[string]source.TableDef{"t": def},
		Warn: func(msg string) { warnings = append(warnings, msg) }}); err != nil {
		t.Fatal(err)
	}
	db := memDB(t)
	sqliteExec(t, db, ddl.String())
	sqliteExec(t, db, "INSERT INTO t DEFAULT VALUES")
	var a int
	var b, c, d, e, f sql.NullString
	var g sql.NullInt64
	if err := db.QueryRow("SELECT a, b, c, d, e, f, g FROM t").Scan(&a, &b, &c, &d, &e, &f, &g); err != nil {
		t.Fatal(err)
	}
	if a != -5 || b.String != "it's" || c.String != "a\nb" || d.String != "2.5" || !e.Valid || f.String != "5" || g.Valid {
		t.Errorf("defaults: %d %q %q %q %q %q %v", a, b.String, c.String, d.String, e.String, f.String, g)
	}
	if got := strings.Join(warnings, "\n"); !strings.Contains(got, "default expression of column `g` is not output") {
		t.Errorf("no warning about the expression default:\n%s", got)
	}
}
