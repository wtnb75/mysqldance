package writer

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

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

// pgSchema is relationalSchema with a view that converts and one that does not,
// and a CHECK constraint, which the PostgreSQL writer does not output, plus a
// foreign key to a non-unique column.
func pgSchema() Schema {
	s := relationalSchema()
	s.Views = []schemafile.View{
		{Name: "v", Create: "CREATE VIEW `v` AS select 1"},
		{Name: "w", Create: "CREATE VIEW `w` AS select date_format(`child`.`created`,'%Y') AS `y` from `child`"},
	}
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
	"CREATE UNIQUE INDEX \"parent__uk_id_code\" ON \"parent\" (\"id\", \"code\");\n" +
	"CREATE VIEW \"v\" AS select 1;\n"

func TestPostgresSchema(t *testing.T) {
	got, warns := writeSchemaText(t, pgSchema())
	if got != wantPostgresDDL {
		t.Errorf("schema differs\n got:\n%s\nwant:\n%s", got, wantPostgresDDL)
	}
	// Nothing PostgreSQL cannot carry over disappears without a word.
	for _, want := range []string{
		"view `w` is not output: DATE_FORMAT() cannot be converted to PostgreSQL", "generated column `twice` is not output",
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

// longFKSchema renames the table child's output foreign key (fk_parent) and, if
// other is set, its foreign key into another database (fk_other, never output).
func longFKSchema(fk, other string) Schema {
	s := pgSchema()
	d := s.Defs["child"]
	d.ForeignKeys[0].Name = fk
	if other != "" {
		d.ForeignKeys[1].Name = other
	}
	s.Defs["child"] = d
	return s
}

func writeConstraintsText(t *testing.T, s Schema) (string, []string, error) {
	t.Helper()
	var warns []string
	s.Warn = func(m string) { warns = append(warns, m) }
	var b bytes.Buffer
	err := NewPostgres(datafile.Options{}).WriteConstraints(&b, s)
	return b.String(), warns, err
}

// MySQL allows 64 characters in a constraint name, PostgreSQL 63 bytes. The name
// is shortened by the rule of the index names, and the warning names both.
func TestPostgresShortensLongForeignKeyName(t *testing.T) {
	long := strings.Repeat("f", 64)
	short := pgShortName(long)
	got, warns, err := writeConstraintsText(t, longFKSchema(long, ""))
	if err != nil {
		t.Fatal(err)
	}
	if want := `ADD CONSTRAINT ` + sqlvalue.QuotePGIdent(short) + ` FOREIGN KEY`; !strings.Contains(got, want) {
		t.Errorf("no %q in:\n%s", want, got)
	}
	if strings.Contains(got, long) {
		t.Errorf("the full name is still in the output:\n%s", got)
	}
	if want := "foreign key `" + long + "` is named `" + short + "` in PostgreSQL"; !containsAny(warns, want) {
		t.Errorf("no warning contains %q; got %q", want, warns)
	}
	// It is the index names' rule (pgShortName), cut to the limit.
	if len(short) != pgMaxIdent || !strings.HasPrefix(short, strings.Repeat("f", 54)+"_") {
		t.Errorf("short = %q (%d bytes)", short, len(short))
	}
}

// A name within the limit is not changed, and says nothing.
func TestPostgresKeepsForeignKeyNameWithinLimit(t *testing.T) {
	name := strings.Repeat("f", pgMaxIdent)
	got, warns, err := writeConstraintsText(t, longFKSchema(name, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `ADD CONSTRAINT "`+name+`" FOREIGN KEY`) {
		t.Errorf("the name was changed:\n%s", got)
	}
	if containsAny(warns, "is named") {
		t.Errorf("a name within the limit was reported: %q", warns)
	}
}

// A foreign key that is not output has no PostgreSQL name, so its length is
// not looked at.
func TestPostgresLongNameOfSkippedForeignKeyIsNotAnError(t *testing.T) {
	long := strings.Repeat("o", 64)
	got, warns, err := writeConstraintsText(t, longFKSchema("fk_parent", long))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, long) || containsAny(warns, "is named") {
		t.Errorf("a foreign key that is not output was shortened:\n%s\n%q", got, warns)
	}
	if !containsAny(warns, "foreign key `"+long+"` references another database") {
		t.Errorf("no warning about the skipped foreign key: %q", warns)
	}
}

// Within a table, a shortened name that is another foreign key's name is an
// error. The error names the name before the cut, since a failed run prints no
// warnings.
func TestPostgresShortenedForeignKeyCollisionIsAnError(t *testing.T) {
	long := strings.Repeat("f", 64)
	s := longFKSchema(long, "")
	d := s.Defs["child"]
	// The second key is output too: it uses the same columns as the first.
	second := d.ForeignKeys[0]
	second.Name = pgShortName(long)
	d.ForeignKeys = append(d.ForeignKeys, second)
	s.Defs["child"] = d
	_, _, err := writeConstraintsText(t, s)
	if err == nil {
		t.Fatal("WriteConstraints succeeded")
	}
	if !strings.Contains(err.Error(), long) {
		t.Errorf("the error does not name the full name: %v", err)
	}
}

// The primary key is a constraint of the table too, named table__PRIMARY.
func TestPostgresForeignKeyNamedLikeThePrimaryKeyIsAnError(t *testing.T) {
	_, _, err := writeConstraintsText(t, longFKSchema("child__PRIMARY", ""))
	if err == nil {
		t.Fatal("WriteConstraints succeeded")
	}
	if !strings.Contains(err.Error(), "child__PRIMARY") {
		t.Errorf("the error does not name the constraint: %v", err)
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
		// MariaDB reports the width of an unsigned TINYINT(1); MySQL 8.0.19+ does
		// not. Both are smallint (README, "PostgreSQL").
		{"tinyint(1) unsigned", "tinyint", "smallint"},
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

var hexSuffix = regexp.MustCompile(`^_[0-9a-f]{8}$`)

// A name within PostgreSQL's limit is kept as it is; a longer one is cut on a
// character boundary and ends in a hash of the whole name.
func TestPgShortName(t *testing.T) {
	at63 := strings.Repeat("a", 63)
	if got := pgShortName(at63); got != at63 {
		t.Errorf("a 63-byte name changed: %q", got)
	}
	for _, name := range []string{
		strings.Repeat("a", 64),
		strings.Repeat("a", 200),
		// 3 bytes per character: the cut at 54 bytes falls between characters, but
		// a prefix of 55 bytes would end in the middle of one.
		strings.Repeat("あ", 25) + "__idx",
		// 4 bytes per character: 54 is not a multiple of 4, so the cut must back off.
		strings.Repeat("😀", 20) + "__idx",
		"é" + strings.Repeat("a", 70),
	} {
		got := pgShortName(name)
		if len(got) > pgMaxIdent {
			t.Errorf("%q: %d bytes, over %d", name, len(got), pgMaxIdent)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%q: result %q is not valid UTF-8", name, got)
		}
		if len(got) < 9 || !hexSuffix.MatchString(got[len(got)-9:]) {
			t.Errorf("%q: result %q does not end in _ and 8 hex digits", name, got)
		}
		if !strings.HasPrefix(name, got[:len(got)-9]) {
			t.Errorf("%q: result %q does not start with a prefix of the name", name, got)
		}
		if again := pgShortName(name); again != got {
			t.Errorf("%q: %q then %q", name, got, again)
		}
	}
	// The rule is part of the output format: a change to it renames indexes in
	// every dump. The hash is sha256("parent__" + 60 "i").
	if got, want := pgShortName("parent__"+strings.Repeat("i", 60)), "parent__"+strings.Repeat("i", 46)+"_a5daa4f3"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The hash is of the whole name, so two names with the same first 54 bytes differ.
	a, b := strings.Repeat("a", 70)+"1", strings.Repeat("a", 70)+"2"
	if pgShortName(a) == pgShortName(b) {
		t.Errorf("%q and %q both became %q", a, b, pgShortName(a))
	}
}

func longIndexSchema(index string) Schema {
	s := pgSchema()
	d := s.Defs["parent"]
	d.Indexes[1].Name = index
	s.Defs["parent"] = d
	return s
}

// An index whose name would pass PostgreSQL's limit once the table name is put
// in front is shortened, and the warning names both.
func TestPostgresShortensLongIndexName(t *testing.T) {
	long := strings.Repeat("i", 60)
	full := "parent__" + long
	short := pgShortName(full)
	got, warns := writeSchemaText(t, longIndexSchema(long))
	if want := "CREATE UNIQUE INDEX " + sqlvalue.QuotePGIdent(short) + ` ON "parent" ("code");` + "\n"; !strings.Contains(got, want) {
		t.Errorf("no %q in:\n%s", want, got)
	}
	if strings.Contains(got, full) {
		t.Errorf("the full name %q is still in the output:\n%s", full, got)
	}
	if want := "index `" + long + "` is named `" + short + "` in PostgreSQL, not `" + full + "`"; !containsAny(warns, want) {
		t.Errorf("no warning contains %q; got %q", want, warns)
	}
	// Short names are untouched, and say nothing.
	if !strings.Contains(got, `"parent__PRIMARY"`) || containsAny(warns, "parent__PRIMARY") {
		t.Errorf("a short name was changed or reported: %q", warns)
	}
}

func TestPostgresShortensLongPrimaryKeyName(t *testing.T) {
	s := pgSchema()
	table := strings.Repeat("t", 60) // within the limit, but table__PRIMARY is not
	s.Tables[1].Name = table
	s.Defs[table] = s.Defs["parent"]
	delete(s.Defs, "parent")
	for i, fk := range s.Defs["child"].ForeignKeys {
		if fk.RefTable == "parent" {
			s.Defs["child"].ForeignKeys[i].RefTable = table
		}
	}
	full := table + "__PRIMARY"
	short := pgShortName(full)
	got, warns := writeSchemaText(t, s)
	if want := "CONSTRAINT " + sqlvalue.QuotePGIdent(short) + " PRIMARY KEY"; !strings.Contains(got, want) {
		t.Errorf("no %q in:\n%s", want, got)
	}
	if want := "index `PRIMARY` is named `" + short + "` in PostgreSQL, not `" + full + "`"; !containsAny(warns, want) {
		t.Errorf("no warning contains %q; got %q", want, warns)
	}
}

func TestPostgresLongIndexNamesAreDeterministic(t *testing.T) {
	long := strings.Repeat("あ", 30)
	a, _ := writeSchemaText(t, longIndexSchema(long))
	b, _ := writeSchemaText(t, longIndexSchema(long))
	if a != b {
		t.Error("two renderings of the same schema differ")
	}
}

// Two long names that start alike stay apart; only a real collision is an error.
func TestPostgresLongIndexNamesThatStartAlike(t *testing.T) {
	s := longIndexSchema(strings.Repeat("i", 60) + "1")
	d := s.Defs["parent"]
	d.Indexes[2].Name = strings.Repeat("i", 60) + "2"
	s.Defs["parent"] = d
	got, _ := writeSchemaText(t, s)
	for _, n := range []string{"1", "2"} {
		short := pgShortName("parent__" + strings.Repeat("i", 60) + n)
		if !strings.Contains(got, sqlvalue.QuotePGIdent(short)) {
			t.Errorf("no index %s in:\n%s", short, got)
		}
	}
}

func TestPostgresShortenedNameCollisionIsAnError(t *testing.T) {
	long := strings.Repeat("i", 60)
	short := pgShortName("parent__" + long)
	s := longIndexSchema(long)
	d := s.Defs["parent"]
	// This index's own name, parent__<x>, is the shortened name above.
	d.Indexes[2].Name = strings.TrimPrefix(short, "parent__")
	s.Defs["parent"] = d
	var b bytes.Buffer
	err := NewPostgres(datafile.Options{}).WriteSchema(&b, s)
	if err == nil {
		t.Fatalf("WriteSchema succeeded:\n%s", b.String())
	}
	// A failed run reports no warnings, so the error has to say what was shortened.
	if !strings.Contains(err.Error(), "parent__"+long) {
		t.Errorf("the error does not name the full name: %v", err)
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
	got := renderParts(t, dataParts(NewPostgres(datafile.Options{}), "d", info, rows))
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
	got := renderParts(t, dataParts(NewPostgres(datafile.Options{}), "d", info, nil))
	if want := wantSettings + "SET search_path = \"d\";\nBEGIN;\nCOMMIT;\n"; len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want one file %q", got, want)
	}
}

func TestPostgresDataListsColumnsWhenAsked(t *testing.T) {
	info, _ := boolSchema()
	info.ListColumns = true
	got := renderParts(t, dataParts(NewPostgres(datafile.Options{}), "d", info, [][]sqlvalue.Value{pgRow("1", "1", "\x01", "a", "<null>")}))
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
			parts := dataParts(NewPostgres(datafile.Options{}), "d", info, [][]sqlvalue.Value{c.row})
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
	parts := dataParts(NewPostgres(datafile.Options{}), "d", info, [][]sqlvalue.Value{row})
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
	parts := dataParts(NewPostgres(datafile.Options{}), "d", info, nil)
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
	parts := dataParts(w, "d", info, rows)
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
