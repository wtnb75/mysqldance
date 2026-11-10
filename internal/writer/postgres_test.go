package writer

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

var (
	_ Writer           = (*Postgres)(nil)
	_ ConstraintWriter = (*Postgres)(nil)
)

const wantSettings = "SET client_encoding = 'UTF8';\n" +
	"SET standard_conforming_strings = on;\n" +
	"SET TimeZone = 'UTC';\n"

func TestPostgresNeedsDefs(t *testing.T) {
	if !NewPostgres(datafile.Options{}).NeedsDefs() {
		t.Error("the PostgreSQL writer builds its DDL from table definitions")
	}
}

// pgSchema is relationalSchema with its views and a CHECK constraint, which the
// PostgreSQL writer does not output, plus a foreign key to a non-unique column.
func pgSchema() Schema {
	s := relationalSchema()
	s.Views = []schemafile.View{{Name: "v", Create: "CREATE VIEW `v` AS select 1"}}
	return s
}

func writeSchemaText(t *testing.T, s Schema) (string, []string) {
	t.Helper()
	var warns []string
	s.Warn = func(m string) { warns = append(warns, m) }
	var b bytes.Buffer
	if err := NewPostgres(datafile.Options{}).WriteSchema(&b, s); err != nil {
		t.Fatal(err)
	}
	return b.String(), warns
}

const wantPostgresDDL = wantSettings +
	"CREATE SCHEMA IF NOT EXISTS \"d\";\n" +
	"SET search_path = \"d\";\n" +
	"CREATE TABLE \"child\" (\n" +
	"  \"id\" integer NOT NULL,\n" +
	"  \"parent_id\" numeric(20,0),\n" +
	"  \"code\" varchar(10) NOT NULL DEFAULT 'x',\n" +
	"  \"qty\" numeric(10,2) NOT NULL DEFAULT 1.50,\n" +
	"  \"created\" timestamp(0) NOT NULL DEFAULT CURRENT_TIMESTAMP,\n" +
	"  \"note\" varchar(100),\n" +
	"  CONSTRAINT \"child__PRIMARY\" PRIMARY KEY (\"id\")\n" +
	");\n" +
	"CREATE INDEX \"child__idx_note\" ON \"child\" (\"note\", \"created\" DESC);\n" +
	"CREATE TABLE \"parent\" (\n" +
	"  \"id\" numeric(20,0) NOT NULL,\n" +
	"  \"code\" varchar(10) NOT NULL,\n" +
	"  CONSTRAINT \"parent__PRIMARY\" PRIMARY KEY (\"id\")\n" +
	");\n" +
	"CREATE UNIQUE INDEX \"parent__uk_code\" ON \"parent\" (\"code\");\n" +
	"CREATE UNIQUE INDEX \"parent__uk_id_code\" ON \"parent\" (\"id\", \"code\");\n"

func TestPostgresSchema(t *testing.T) {
	got, warns := writeSchemaText(t, pgSchema())
	if got != wantPostgresDDL {
		t.Errorf("schema differs\n got:\n%s\nwant:\n%s", got, wantPostgresDDL)
	}
	// Nothing PostgreSQL cannot carry over disappears without a word.
	for _, want := range []string{
		"view `v` is not output", "generated column `twice` is not output",
		"ON UPDATE of column `created` is not output", "index `ft_note` (FULLTEXT) is not output",
		"index `fn` has an expression key part", "index `idx_note` covers a column prefix",
		"CHECK constraint `ck_qty` is not output",
	} {
		if !containsAny(warns, want) {
			t.Errorf("no warning contains %q; got %q", want, warns)
		}
	}
}

func containsAny(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// MySQL takes PRIMARY KEY (a DESC); PostgreSQL's syntax has no order there. The
// order of a primary key means nothing to a restore, so it is dropped, said.
func TestPostgresPrimaryKeyDescIsDroppedWithAWarning(t *testing.T) {
	s := pgSchema()
	d := s.Defs["parent"]
	d.Indexes[0].Parts[0].Desc = true
	s.Defs["parent"] = d
	got, warns := writeSchemaText(t, s)
	if !strings.Contains(got, `CONSTRAINT "parent__PRIMARY" PRIMARY KEY ("id")`) {
		t.Errorf("the primary key keeps its DESC:\n%s", got)
	}
	if !containsAny(warns, "primary key") {
		t.Errorf("no warning about the primary key order: %q", warns)
	}
}

func TestPostgresSchemaDeterministic(t *testing.T) {
	a, _ := writeSchemaText(t, pgSchema())
	b, _ := writeSchemaText(t, pgSchema())
	if a != b {
		t.Error("two renderings of the same schema differ")
	}
}

func TestPostgresConstraints(t *testing.T) {
	s := pgSchema()
	var warns []string
	s.Warn = func(m string) { warns = append(warns, m) }
	var b bytes.Buffer
	if err := NewPostgres(datafile.Options{}).WriteConstraints(&b, s); err != nil {
		t.Fatal(err)
	}
	want := wantSettings + "SET search_path = \"d\";\n" +
		"ALTER TABLE \"child\" ADD CONSTRAINT \"fk_parent\" FOREIGN KEY (\"parent_id\", \"code\") " +
		"REFERENCES \"parent\" (\"id\", \"code\") ON DELETE CASCADE ON UPDATE RESTRICT;\n"
	if b.String() != want {
		t.Errorf("constraints differ\n got:\n%s\nwant:\n%s", b.String(), want)
	}
	if !containsAny(warns, "foreign key `fk_other` references another database") {
		t.Errorf("no warning about the foreign key into another database: %q", warns)
	}
}

// With no foreign key the constraints file is still written (the header only),
// so a dump always has the same set of files.
func TestPostgresConstraintsWithoutForeignKeys(t *testing.T) {
	var b bytes.Buffer
	if err := NewPostgres(datafile.Options{}).WriteConstraints(&b, typesSchema()); err != nil {
		t.Fatal(err)
	}
	if want := wantSettings + "SET search_path = \"d\";\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// PostgreSQL needs a unique index behind the referenced columns; InnoDB does
// not. Such a foreign key is reported and left out, not written to fail at
// restore.
func TestPostgresConstraintsNeedAUniqueTarget(t *testing.T) {
	s := pgSchema()
	child := s.Defs["child"]
	child.ForeignKeys = []source.ForeignKey{{Name: "fk_code", Columns: []string{"code"}, RefSchema: "d",
		RefTable: "parent", RefColumns: []string{"code", "id"}[:1], OnUpdate: "RESTRICT", OnDelete: "RESTRICT"}}
	parent := s.Defs["parent"]
	// Drop the single-column unique key on code: only (id, code) is left.
	parent.Indexes = []source.IndexDef{parent.Indexes[0], parent.Indexes[2]}
	s.Defs["child"], s.Defs["parent"] = child, parent
	var warns []string
	s.Warn = func(m string) { warns = append(warns, m) }
	var b bytes.Buffer
	if err := NewPostgres(datafile.Options{}).WriteConstraints(&b, s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "fk_code") {
		t.Errorf("the foreign key was written:\n%s", b.String())
	}
	if !containsAny(warns, "foreign key `fk_code`") || !containsAny(warns, "not a primary key or unique key") {
		t.Errorf("warnings = %q", warns)
	}
}

func TestPostgresTypes(t *testing.T) {
	cases := []struct{ typ, data, want string }{
		{"tinyint(1)", "tinyint", "boolean"},
		{"tinyint(1) unsigned", "tinyint", "boolean"},
		{"tinyint(4)", "tinyint", "smallint"},
		{"tinyint unsigned", "tinyint", "smallint"},
		{"smallint", "smallint", "smallint"},
		{"smallint unsigned", "smallint", "integer"},
		{"mediumint unsigned", "mediumint", "integer"},
		{"int", "int", "integer"},
		{"int unsigned", "int", "bigint"},
		{"bigint", "bigint", "bigint"},
		{"bigint unsigned", "bigint", "numeric(20,0)"},
		{"year", "year", "smallint"},
		{"bit(1)", "bit", "bit(1)"},
		{"bit(64)", "bit", "bit(64)"},
		{"decimal(65,30)", "decimal", "numeric(65,30)"},
		{"float", "float", "real"},
		{"double", "double", "double precision"},
		{"char(5)", "char", "varchar(5)"},
		{"char(0)", "char", "text"},
		{"varchar(255)", "varchar", "varchar(255)"},
		{"text", "text", "text"},
		{"longtext", "longtext", "text"},
		{"enum('a','b')", "enum", "text"},
		{"set('a','b')", "set", "text"},
		{"json", "json", "json"},
		{"binary(4)", "binary", "bytea"},
		{"varbinary(10)", "varbinary", "bytea"},
		{"longblob", "longblob", "bytea"},
		{"geometry", "geometry", "bytea"},
		{"date", "date", "date"},
		{"datetime", "datetime", "timestamp(0)"},
		{"datetime(6)", "datetime", "timestamp(6)"},
		{"timestamp", "timestamp", "timestamptz(0)"},
		{"timestamp(3)", "timestamp", "timestamptz(3)"},
		{"time", "time", "interval(0)"},
		{"time(6)", "time", "interval(6)"},
	}
	for _, c := range cases {
		got, _, err := pgColumnType(source.ColumnDef{Name: "c", Type: c.typ, DataType: c.data})
		if err != nil {
			t.Errorf("%s: %v", c.typ, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.typ, got, c.want)
		}
	}
	if _, _, err := pgColumnType(source.ColumnDef{Name: "c", Type: "uuid", DataType: "uuid"}); err == nil {
		t.Error("an unknown type was accepted")
	}
}

func TestPostgresSchemaErrors(t *testing.T) {
	long := strings.Repeat("x", 64)
	cases := map[string]func(*Schema){
		"no definition":             func(s *Schema) { delete(s.Defs, "parent") },
		"unknown type":              func(s *Schema) { setType(s, "parent", "code", "uuid") },
		"table name over 63 bytes":  func(s *Schema) { s.Tables[1].Name = long; s.Defs[long] = s.Defs["parent"] },
		"schema name over 63 bytes": func(s *Schema) { s.DB = long },
		"schema name starts pg_":    func(s *Schema) { s.DB = "pg_x" },
		"index name over 63 bytes": func(s *Schema) {
			d := s.Defs["parent"]
			d.Indexes[1].Name = strings.Repeat("i", 60)
			s.Defs["parent"] = d
		},
		"column name over 63 bytes": func(s *Schema) {
			d := s.Defs["parent"]
			d.Columns[1].Name = long
			s.Defs["parent"] = d
			s.Tables[1].Columns[1].Name = long
		},
		"index name collision": func(s *Schema) {
			// parent__uk_code is also the name this index would get.
			d := s.Defs["parent"]
			d.Indexes[2].Name = "uk_code"
			s.Defs["parent"] = d
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := pgSchema()
			mutate(&s)
			var b bytes.Buffer
			if err := NewPostgres(datafile.Options{}).WriteSchema(&b, s); err == nil {
				t.Errorf("WriteSchema succeeded:\n%s", b.String())
			}
		})
	}
}

func setType(s *Schema, table, col, typ string) {
	d := s.Defs[table]
	for i := range d.Columns {
		if d.Columns[i].Name == col {
			d.Columns[i].Type, d.Columns[i].DataType = typ, typ
		}
	}
	s.Defs[table] = d
}

// boolSchema is a table with a boolean, a bit, a text and a timestamp column.
func boolSchema() (source.TableInfo, source.TableDef) {
	info := source.TableInfo{Name: "t", OrderBy: []string{"id"}, OrderByTypes: []string{"int"}}
	def := source.TableDef{Indexes: []source.IndexDef{
		{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id"}}}}}
	for _, c := range []struct{ name, typ, data, driver string }{
		{"id", "int", "int", "INT"},
		{"ok", "tinyint(1)", "tinyint", "TINYINT"},
		{"bits", "bit(5)", "bit", "BIT"},
		{"s", "text", "text", "TEXT"},
		{"ts", "timestamp(3)", "timestamp", "TIMESTAMP"},
	} {
		info.Columns = append(info.Columns, sqlvalue.Column{Name: c.name, TypeName: c.driver})
		def.Columns = append(def.Columns, source.ColumnDef{Name: c.name, Type: c.typ, DataType: c.data, Nullable: c.name != "id"})
	}
	info.Def = &def
	return info, def
}

func pgRow(id, ok, bits, s, ts string) []sqlvalue.Value {
	cell := func(typ, v string) sqlvalue.Value { return sqlvalue.NewValue(typ, []byte(v), v == "<null>") }
	return []sqlvalue.Value{cell("INT", id), cell("TINYINT", ok), cell("BIT", bits), cell("TEXT", s), cell("TIMESTAMP", ts)}
}

func renderParts(t *testing.T, parts []Part) []string {
	t.Helper()
	var out []string
	for _, p := range parts {
		var b bytes.Buffer
		if err := p(&b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b.String())
	}
	return out
}

func TestPostgresData(t *testing.T) {
	info, _ := boolSchema()
	rows := [][]sqlvalue.Value{
		pgRow("1", "1", "\x15", "it's\nnew", "2024-01-02 03:04:05.678"),
		pgRow("2", "0", "\x00", "", "<null>"),
		pgRow("3", "<null>", "<null>", "<null>", "<null>"),
	}
	got := renderParts(t, NewPostgres(datafile.Options{}).DataParts("d", info, rows))
	want := wantSettings + "SET search_path = \"d\";\nBEGIN;\n" +
		"INSERT INTO \"t\" VALUES\n" +
		"(1,true,B'10101',E'it''s\\nnew','2024-01-02 03:04:05.678'),\n" +
		"(2,false,B'00000','',NULL),\n" +
		"(3,NULL,NULL,NULL,NULL);\n" +
		"COMMIT;\n"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPostgresDataEmptyTable(t *testing.T) {
	info, _ := boolSchema()
	got := renderParts(t, NewPostgres(datafile.Options{}).DataParts("d", info, nil))
	if want := wantSettings + "SET search_path = \"d\";\nBEGIN;\nCOMMIT;\n"; len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want one file %q", got, want)
	}
}

func TestPostgresDataListsColumnsWhenAsked(t *testing.T) {
	info, _ := boolSchema()
	info.ListColumns = true
	got := renderParts(t, NewPostgres(datafile.Options{}).DataParts("d", info, [][]sqlvalue.Value{pgRow("1", "1", "\x01", "a", "<null>")}))
	if !strings.Contains(got[0], `INSERT INTO "t" ("id", "ok", "bits", "s", "ts") VALUES`) {
		t.Errorf("columns are not listed:\n%s", got[0])
	}
}

// A value PostgreSQL cannot hold fails the file with the table and column.
func TestPostgresDataRejects(t *testing.T) {
	info, _ := boolSchema()
	cases := map[string]struct {
		row  []sqlvalue.Value
		want string
	}{
		"boolean 2":   {pgRow("1", "2", "\x01", "a", "<null>"), "`ok`"},
		"NUL":         {pgRow("1", "1", "\x01", "a\x00", "<null>"), "`s`"},
		"zero date":   {pgRow("1", "1", "\x01", "a", "0000-00-00 00:00:00"), "`ts`"},
		"bit too big": {pgRow("1", "1", "\xff", "a", "<null>"), "`bits`"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			parts := NewPostgres(datafile.Options{}).DataParts("d", info, [][]sqlvalue.Value{c.row})
			if len(parts) == 0 {
				t.Fatal("no part")
			}
			var err error
			for _, p := range parts {
				if err = p(&bytes.Buffer{}); err != nil {
					break
				}
			}
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "`t`") {
				t.Errorf("got %v, want an error naming table `t` and column %s", err, c.want)
			}
		})
	}
}

// A row with more values than the table has output columns is an error, not an
// empty record.
func TestPostgresDataRowLongerThanColumns(t *testing.T) {
	info, _ := boolSchema()
	row := append(pgRow("1", "1", "\x01", "a", "<null>"), sqlvalue.NewValue("INT", []byte("9"), false))
	parts := NewPostgres(datafile.Options{}).DataParts("d", info, [][]sqlvalue.Value{row})
	var err error
	for _, p := range parts {
		if err = p(&bytes.Buffer{}); err != nil {
			break
		}
	}
	if err == nil {
		t.Error("a row with an extra value was written")
	}
}

func TestPostgresDataWithoutDefinition(t *testing.T) {
	info, _ := boolSchema()
	info.Def = nil
	parts := NewPostgres(datafile.Options{}).DataParts("d", info, nil)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want one that reports the error", len(parts))
	}
	if err := parts[0](&bytes.Buffer{}); err == nil {
		t.Error("a table without a definition was written")
	}
}

func TestPostgresDataSplitsLikeMySQL(t *testing.T) {
	info, _ := boolSchema()
	var rows [][]sqlvalue.Value
	for i := 1; i <= 100; i++ {
		rows = append(rows, pgRow(fmt.Sprint(i), "1", "\x01", "row", "<null>"))
	}
	w := NewPostgres(datafile.Options{RowsPerFile: 20, RowsPerInsert: 5})
	parts := w.DataParts("d", info, rows)
	if len(parts) < 2 {
		t.Fatalf("got %d parts, want the table split", len(parts))
	}
	files := renderParts(t, parts)
	n := 0
	for _, f := range files {
		n += strings.Count(f, "\n(")
		if !strings.HasSuffix(f, ";\nCOMMIT;\n") {
			t.Errorf("a part does not end with COMMIT:\n%s", f)
		}
	}
	if n != 100 {
		t.Errorf("parts hold %d rows, want 100", n)
	}
}
