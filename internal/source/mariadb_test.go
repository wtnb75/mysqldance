package source

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/schemafile"
)

// TestViewDepsAcrossLevels runs on every server: the view dependencies come
// from VIEW_TABLE_USAGE on MySQL and from VIEW_DEFINITION on MariaDB, and must
// be the same. The names make alphabetical order the wrong creation order
// (a_top needs m_mid needs z_base).
func TestViewDepsAcrossLevels(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE t (id INT PRIMARY KEY)",
		"CREATE TABLE u (id INT PRIMARY KEY)",
		"CREATE VIEW z_base AS SELECT id FROM t",
		"CREATE VIEW m_mid AS SELECT id FROM z_base",
		"CREATE VIEW a_top AS SELECT m.id FROM m_mid m JOIN u ON u.id = m.id",
		"CREATE VIEW b_two AS SELECT a.id FROM a_top a JOIN z_base z ON z.id = a.id",
		"CREATE VIEW c_plain AS SELECT id FROM u",
	)
	s := openSnapshot(t, cfg, db)
	views, deps, err := s.Views(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"a_top": {"m_mid"}, "b_two": {"a_top", "z_base"}, "m_mid": {"z_base"}}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("deps = %v, want %v", deps, want)
	}
	names := make([]string, len(views))
	for i, v := range views {
		names[i] = v.Name
	}
	if got, want := schemafile.OrderViews(names, deps), []string{"c_plain", "z_base", "m_mid", "a_top", "b_two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("OrderViews = %v, want %v", got, want)
	}
}

// TestUnsupportedTableTypes: a SEQUENCE and a SYSTEM VERSIONED table are not
// BASE TABLE or VIEW. They must stop the dump, not vanish from it.
func TestUnsupportedTableTypes(t *testing.T) {
	for _, tc := range []struct{ name, create, typ string }{
		{"seq", "CREATE SEQUENCE seq", "SEQUENCE"},
		{"hist", "CREATE TABLE hist (id INT PRIMARY KEY, v INT) WITH SYSTEM VERSIONING", "SYSTEM VERSIONED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, db, conn := testDB(t)
			if !isMariaDB(t, conn) {
				t.Skip("MariaDB only")
			}
			mustExec(t, conn, "CREATE TABLE plain (id INT PRIMARY KEY)", tc.create)
			s := openSnapshot(t, cfg, db)
			ctx := context.Background()
			for what, call := range map[string]func() error{
				"Tables": func() error { _, _, err := s.Tables(ctx); return err },
				"Views":  func() error { _, _, err := s.Views(ctx); return err },
				"Defs":   func() error { _, err := s.Defs(ctx); return err },
			} {
				err := call()
				if err == nil {
					t.Errorf("%s succeeded, want an error for the %s", what, tc.typ)
					continue
				}
				if !strings.Contains(err.Error(), "`"+tc.name+"`") || !strings.Contains(err.Error(), tc.typ) {
					t.Errorf("%s error = %q, want the name %s and the type %s", what, err, tc.name, tc.typ)
				}
			}
		})
	}
}

// TestMariaDBDefs reads the defaults MariaDB reports as the text of the
// DEFAULT clause.
func TestMariaDBDefs(t *testing.T) {
	cfg, db, conn := testDB(t)
	if !isMariaDB(t, conn) {
		t.Skip("MariaDB only")
	}
	mustExec(t, conn,
		"CREATE TABLE d ("+
			" id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,"+
			" a VARCHAR(20) DEFAULT NULL,"+
			" b VARCHAR(20) DEFAULT 'x',"+
			" c VARCHAR(20) DEFAULT 'a\\\\b',"+
			" e VARCHAR(20) DEFAULT '',"+
			" f VARCHAR(20) DEFAULT 'NULL',"+
			" g VARCHAR(20) DEFAULT 'it''s',"+
			" h INT DEFAULT -5,"+
			" i DECIMAL(5,2) DEFAULT -1.50,"+
			" j BIT(5) DEFAULT b'10101',"+
			" m DATE DEFAULT '2020-01-02',"+
			" n TIMESTAMP NULL DEFAULT current_timestamp(),"+
			" o DATETIME(3) DEFAULT current_timestamp(3) ON UPDATE current_timestamp(3),"+
			" p VARCHAR(36) DEFAULT uuid(),"+
			" q INT DEFAULT (r + 1),"+
			" r INT NOT NULL)",
	)
	s := openSnapshot(t, cfg, db)
	defs, err := s.Defs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type def struct {
		val  string
		none bool
		expr bool
	}
	byName := map[string]ColumnDef{}
	for _, c := range defs["d"].Columns {
		byName[c.Name] = c
	}
	for name, want := range map[string]def{
		"id": {none: true},
		"a":  {none: true},
		"r":  {none: true},
		"b":  {val: "x"},
		"c":  {val: `a\b`},
		"e":  {val: ""},
		"f":  {val: "NULL"},
		"g":  {val: "it's"},
		"h":  {val: "-5"},
		"i":  {val: "-1.50"},
		"j":  {val: "b'10101'"},
		"m":  {val: "2020-01-02"},
		"n":  {val: "current_timestamp()", expr: true},
		"o":  {val: "current_timestamp(3)", expr: true},
		"p":  {val: "uuid()", expr: true},
		"q":  {val: "(`r` + 1)", expr: true},
	} {
		c, ok := byName[name]
		if !ok {
			t.Errorf("no column %s", name)
			continue
		}
		if want.none {
			if c.Default != nil || c.DefaultExpr {
				t.Errorf("%s: default = %v (expr %v), want none", name, deref(c.Default), c.DefaultExpr)
			}
			continue
		}
		if c.Default == nil || *c.Default != want.val || c.DefaultExpr != want.expr {
			t.Errorf("%s: default = %v (expr %v), want %q (expr %v)", name, deref(c.Default), c.DefaultExpr, want.val, want.expr)
		}
	}
	if !byName["o"].OnUpdate {
		t.Errorf("o: OnUpdate = false, want true")
	}
}
