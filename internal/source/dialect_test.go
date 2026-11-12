package source

import (
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDialectFor(t *testing.T) {
	// "8" has no minor part; it was accepted before 5.7 was.
	for _, v := range []string{"8.0.46", "8.4.13", "9.1.0", "8.0.46-0ubuntu0.22.04.1", "8", "9"} {
		d, err := dialectFor(v)
		if err != nil {
			t.Errorf("dialectFor(%q): %v", v, err)
			continue
		}
		if !slices.Contains(d.session, "SET SESSION show_create_table_verbosity = 0") {
			t.Errorf("dialectFor(%q).session = %q, want show_create_table_verbosity", v, d.session)
		}
	}
	// MariaDB reports its own version, sometimes behind the "5.5.5-" prefix of
	// the replication handshake. It has no show_create_table_verbosity.
	for _, v := range []string{"10.11.19-MariaDB-ubu2204", "11.4.13-MariaDB-ubu2404", "5.5.5-10.11.19-MariaDB", "10.5.0-mariadb"} {
		d, err := dialectFor(v)
		if err != nil {
			t.Errorf("dialectFor(%q): %v", v, err)
			continue
		}
		for _, stmt := range d.session {
			if strings.Contains(stmt, "show_create_table_verbosity") {
				t.Errorf("dialectFor(%q).session has %q, which MariaDB does not know", v, stmt)
			}
		}
		for _, want := range []string{"SET SESSION sql_mode = ''", "SET SESSION sql_quote_show_create = 1"} {
			if !slices.Contains(d.session, want) {
				t.Errorf("dialectFor(%q).session = %q, want %q", v, d.session, want)
			}
		}
	}
	for _, v := range []string{"5.5.62", "5.1.73", "5.0.96", "5", "5.8.0", "7.9.0", "garbage", ""} {
		_, err := dialectFor(v)
		if err == nil {
			t.Errorf("dialectFor(%q) succeeded, want an error", v)
			continue
		}
		if !strings.Contains(err.Error(), "MySQL 5.6 or later") {
			t.Errorf("dialectFor(%q) error = %q, want it to say \"MySQL 5.6 or later\"", v, err)
		}
	}
}

// TestDialectForMySQL56 covers MySQL 5.6 (5.6.51 was checked): it reads as 5.7
// does, with the same dialect and no branch of its own.
func TestDialectForMySQL56(t *testing.T) {
	want, err := dialectFor("5.7.44")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"5.6.51", "5.6.51-log", "5.6.40-0ubuntu0.14.04.1", "5.6"} {
		d, err := dialectFor(v)
		if err != nil {
			t.Errorf("dialectFor(%q): %v", v, err)
			continue
		}
		// Funcs cannot be compared with DeepEqual; the session settings (which
		// must leave out show_create_table_verbosity, unknown to 5.6) and the
		// view and default readers stand for the whole dialect.
		if !slices.Equal(d.session, want.session) {
			t.Errorf("dialectFor(%q).session = %q, want the 5.7 one %q", v, d.session, want.session)
		}
		if reflect.ValueOf(d.viewDeps).Pointer() != reflect.ValueOf(viewDefinitions).Pointer() {
			t.Errorf("dialectFor(%q).viewDeps is not viewDefinitions (5.6 has no VIEW_TABLE_USAGE)", v)
		}
		if reflect.ValueOf(d.columnDefault).Pointer() != reflect.ValueOf(want.columnDefault).Pointer() {
			t.Errorf("dialectFor(%q).columnDefault is not the 5.7 reader", v)
		}
	}
}

// TestDialectForMySQL57 covers MySQL 5.7 (5.7.44 was checked): no
// show_create_table_verbosity (5.7.21 and earlier lack it, so 5.7 never sets
// it), and views are ordered from VIEW_DEFINITION.
func TestDialectForMySQL57(t *testing.T) {
	for _, v := range []string{"5.7.44", "5.7.21", "5.7.44-log", "5.7.44-0ubuntu0.18.04.1"} {
		d, err := dialectFor(v)
		if err != nil {
			t.Errorf("dialectFor(%q): %v", v, err)
			continue
		}
		for _, stmt := range d.session {
			if strings.Contains(stmt, "show_create_table_verbosity") {
				t.Errorf("dialectFor(%q).session has %q, which 5.7.21 and earlier do not know", v, stmt)
			}
		}
		for _, want := range []string{"SET SESSION sql_mode = ''", "SET SESSION sql_quote_show_create = 1", "SET NAMES utf8mb4"} {
			if !slices.Contains(d.session, want) {
				t.Errorf("dialectFor(%q).session = %q, want %q", v, d.session, want)
			}
		}
		if reflect.ValueOf(d.viewDeps).Pointer() != reflect.ValueOf(viewDefinitions).Pointer() {
			t.Errorf("dialectFor(%q).viewDeps is not viewDefinitions (5.7 has no VIEW_TABLE_USAGE)", v)
		}
	}
}

// TestMySQL57ColumnDefault covers COLUMN_DEFAULT on 5.7.44: EXTRA has no
// DEFAULT_GENERATED, so only DATA_TYPE tells an expression from a string.
func TestMySQL57ColumnDefault(t *testing.T) {
	d, err := dialectFor("5.7.44")
	if err != nil {
		t.Fatal(err)
	}
	val := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	tests := []struct {
		name     string
		raw      sql.NullString
		dataType string
		extra    string
		want     *string
		wantExp  bool
	}{
		{"no default", sql.NullString{}, "int", "", nil, false},
		{"plain", val("x"), "varchar", "", new("x"), false},
		{"timestamp", val("CURRENT_TIMESTAMP"), "timestamp", "", new("CURRENT_TIMESTAMP"), true},
		{"timestamp with precision", val("CURRENT_TIMESTAMP(6)"), "timestamp", "", new("CURRENT_TIMESTAMP(6)"), true},
		{"datetime on update", val("CURRENT_TIMESTAMP(3)"), "datetime", "on update CURRENT_TIMESTAMP(3)", new("CURRENT_TIMESTAMP(3)"), true},
		{"varchar string stays a string", val("CURRENT_TIMESTAMP"), "varchar", "", new("CURRENT_TIMESTAMP"), false},
		{"char string stays a string", val("CURRENT_TIMESTAMP(6)"), "char", "", new("CURRENT_TIMESTAMP(6)"), false},
		{"timestamp constant", val("2020-01-02 03:04:05"), "timestamp", "", new("2020-01-02 03:04:05"), false},
		{"datetime zero", val("0000-00-00 00:00:00"), "datetime", "", new("0000-00-00 00:00:00"), false},
	}
	for _, tc := range tests {
		got, isExp := d.columnDefault(tc.raw, tc.dataType, tc.extra)
		if !reflect.DeepEqual(got, tc.want) || isExp != tc.wantExp {
			t.Errorf("%s: columnDefault(%q, %q) = %v, %v; want %v, %v", tc.name, tc.raw.String, tc.dataType, deref(got), isExp, deref(tc.want), tc.wantExp)
		}
	}
}

func TestCheckTableType(t *testing.T) {
	for _, typ := range []string{"BASE TABLE", "VIEW"} {
		if err := checkTableType("t", typ); err != nil {
			t.Errorf("checkTableType(%q): %v", typ, err)
		}
	}
	// A type that is neither would be left out of the dump without a word.
	for _, typ := range []string{"SEQUENCE", "SYSTEM VERSIONED", "SYSTEM VIEW", ""} {
		err := checkTableType("t", typ)
		if err == nil {
			t.Errorf("checkTableType(%q) succeeded, want an error", typ)
			continue
		}
		if !strings.Contains(err.Error(), "`t`") || !strings.Contains(err.Error(), typ) {
			t.Errorf("checkTableType(%q) = %q, want the table name and the type", typ, err)
		}
	}
}

//go:fix inline
func TestColumnDefault(t *testing.T) {
	null := sql.NullString{}
	val := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	tests := []struct {
		name      string
		raw       sql.NullString
		extra     string
		want      *string
		wantIsExp bool
	}{
		{"no default", null, "", nil, false},
		{"plain", val("x"), "", new("x"), false},
		{"expression", val("uuid()"), "DEFAULT_GENERATED", new("uuid()"), true},
		{"timestamp", val("CURRENT_TIMESTAMP"), "DEFAULT_GENERATED on update CURRENT_TIMESTAMP", new("CURRENT_TIMESTAMP"), true},
	}
	for _, tc := range tests {
		got, isExp := mysql8.columnDefault(tc.raw, "timestamp", tc.extra)
		if !reflect.DeepEqual(got, tc.want) || isExp != tc.wantIsExp {
			t.Errorf("mysql %s: = %v, %v; want %v, %v", tc.name, deref(got), isExp, deref(tc.want), tc.wantIsExp)
		}
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// TestMariaDBColumnDefault covers COLUMN_DEFAULT as MariaDB reports it
// (checked on 10.11 and 11.4): the text of the DEFAULT clause, not a value.
func TestMariaDBColumnDefault(t *testing.T) {
	d, err := dialectFor("11.4.13-MariaDB")
	if err != nil {
		t.Fatal(err)
	}
	val := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	tests := []struct {
		name    string
		raw     sql.NullString
		want    *string
		wantExp bool
	}{
		{"no default (NOT NULL)", sql.NullString{}, nil, false},
		{"DEFAULT NULL is the text NULL", val("NULL"), nil, false},
		{"string", val("'x'"), new("x"), false},
		{"empty string", val("''"), new(""), false},
		{"the string NULL is quoted", val("'NULL'"), new("NULL"), false},
		{"doubled quote", val("'it''s'"), new("it's"), false},
		{"quote only", val("'''quoted'''"), new("'quoted'"), false},
		{"backslash", val(`'a\\b'`), new(`a\b`), false},
		{"newline", val(`'l1\nl2'`), new("l1\nl2"), false},
		{"escaped quote", val(`'a\'b'`), new("a'b"), false},
		{"nul and ctrl-z", val(`'a\0b\Zc'`), new("a\x00b\x1ac"), false},
		{"a string that looks like hex", val("'0x41'"), new("0x41"), false},
		{"integer", val("0"), new("0"), false},
		{"negative integer", val("-5"), new("-5"), false},
		{"decimal", val("-1.50"), new("-1.50"), false},
		{"float", val("1.5"), new("1.5"), false},
		{"exponent", val("1e-10"), new("1e-10"), false},
		{"bit", val("b'10101'"), new("b'10101'"), false},
		{"date is quoted", val("'2020-01-02'"), new("2020-01-02"), false},
		{"current_timestamp", val("current_timestamp()"), new("current_timestamp()"), true},
		{"current_timestamp with precision", val("current_timestamp(3)"), new("current_timestamp(3)"), true},
		{"function", val("uuid()"), new("uuid()"), true},
		{"parenthesized expression", val("(`r` + 1)"), new("(`r` + 1)"), true},
	}
	for _, tc := range tests {
		got, isExp := d.columnDefault(tc.raw, "varchar", "")
		if !reflect.DeepEqual(got, tc.want) || isExp != tc.wantExp {
			t.Errorf("%s: columnDefault(%q) = %v, %v; want %v, %v", tc.name, tc.raw.String, deref(got), isExp, deref(tc.want), tc.wantExp)
		}
	}
	// ON UPDATE and AUTO_INCREMENT in EXTRA do not make a default an expression.
	if got, isExp := d.columnDefault(val("'x'"), "varchar", "on update current_timestamp()"); got == nil || *got != "x" || isExp {
		t.Errorf("EXTRA changed a quoted default: %v, %v", deref(got), isExp)
	}
}

func TestViewForeignRefs(t *testing.T) {
	views := []string{"v1", "v2", "v3", "v4", "v5"}
	defs := map[string]string{
		// A table of another database, read in a column and in the FROM clause.
		"v1": "select `db2`.`t`.`id` AS `id` from `db2`.`t`",
		// An alias that is not a database, and the database itself.
		"v2": "select `x`.`id` AS `id` from `pr`.`t` `x`",
		// Two databases (case-insensitive, as VIEW_DEFINITION may spell them
		// under lower_case_table_names=2), and the system schemas, which exist on
		// every server and are never a restore-order problem.
		"v3": "select 1 from `pr`.`t`, `DB2`.`u`, `db3`.`z`, `mysql`.`user`, `information_schema`.`TABLES`, `sys`.`x`",
		// A database whose name starts with the name of another is not that one.
		"v4": "select 1 from `db20`.`t`",
		// A backtick inside a name is doubled.
		"v5": "select 1 from `we``ird`.`t`",
	}
	schemas := []string{"db3", "pr", "db2", "db20", "mysql", "information_schema", "sys", "we`ird"}
	got := viewForeignRefs(defs, "pr", views, schemas)
	want := []viewDep{{"v1", "db2"}, {"v3", "db2"}, {"v3", "db3"}, {"v4", "db20"}, {"v5", "we`ird"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("viewForeignRefs = %v, want %v", got, want)
	}
}

func TestViewRefs(t *testing.T) {
	views := []string{"a_top", "v1", "v2", "weird`name"}
	defs := map[string]string{
		// Names are qualified with the schema, as information_schema.VIEWS shows them.
		"a_top":       "select `x`.`id` AS `id` from (`pr`.`v2` `x` join `pr`.`t` `y` on(`y`.`id` = `x`.`id`))",
		"v1":          "select `pr`.`t`.`id` AS `id` from `pr`.`t`",
		"v2":          "select `v1`.`id` AS `id` from `pr`.`v1`",
		"weird`name":  "select `id` from `pr`.`t`",
		"unqualified": "",
	}
	got := viewRefs(defs, "pr", views)
	want := []viewDep{{"a_top", "v2"}, {"v2", "v1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("viewRefs = %v, want %v", got, want)
	}

	// A view named like a prefix of another is not a reference to it, a view in
	// another schema is not either, and a quote inside a name is doubled.
	defs = map[string]string{
		"v1":         "select 1 from `pr`.`v10`, `other`.`v2`",
		"v10":        "select 1 from `pr`.`weird``name`",
		"v2":         "select 1",
		"weird`name": "select 1",
	}
	got = viewRefs(defs, "pr", []string{"v1", "v10", "v2", "weird`name"})
	want = []viewDep{{"v1", "v10"}, {"v10", "weird`name"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("viewRefs = %v, want %v", got, want)
	}

	// With lower_case_table_names=2 the definition may spell a name in another
	// case than TABLE_NAME does; missing the reference would misorder the views.
	defs = map[string]string{"Top": "select 1 from `PR`.`base_V`", "base_v": "select 1"}
	got = viewRefs(defs, "pr", []string{"Top", "base_v"})
	want = []viewDep{{"Top", "base_v"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("viewRefs (case) = %v, want %v", got, want)
	}
}
