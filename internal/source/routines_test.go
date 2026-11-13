package source

import (
	"context"
	"strings"
	"testing"
)

func TestRoutines(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE a (id INT PRIMARY KEY)",
		"CREATE FUNCTION z_add(x INT) RETURNS INT DETERMINISTIC RETURN x + 1",
		"CREATE FUNCTION a_neg(x INT) RETURNS INT DETERMINISTIC RETURN -x",
		"CREATE PROCEDURE m_fill() BEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND",
	)
	s := openSnapshot(t, cfg, db)

	got, err := s.Routines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Procedures before functions, then by name in byte order.
	var names []string
	for _, r := range got {
		names = append(names, r.Type+" "+r.Name)
	}
	if want := "PROCEDURE m_fill|FUNCTION a_neg|FUNCTION z_add"; strings.Join(names, "|") != want {
		t.Fatalf("routines = %q, want %q", strings.Join(names, "|"), want)
	}
	for _, r := range got {
		if !strings.HasPrefix(r.Create, "CREATE ") || !strings.Contains(r.Create, r.Type+" ") || !strings.Contains(r.Create, r.Name) {
			t.Errorf("routine %s Create = %q", r.Name, r.Create)
		}
		// Names in the dumped database stay unqualified so the dump can be
		// restored under another database name.
		if strings.Contains(r.Create, db) {
			t.Errorf("routine %s Create names the database: %q", r.Name, r.Create)
		}
	}
	if !strings.Contains(got[0].Create, "SET @a = 1;") {
		t.Errorf("procedure body not kept as written: %q", got[0].Create)
	}
}

func TestRoutinesKeepSQLMode(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"SET SESSION sql_mode = 'ANSI_QUOTES'",
		`CREATE FUNCTION f() RETURNS VARCHAR(10) DETERMINISTIC RETURN "x"`,
	)
	s := openSnapshot(t, cfg, db)
	got, err := s.Routines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].SQLMode, "ANSI_QUOTES") {
		t.Fatalf("routines = %+v, want one created under ANSI_QUOTES", got)
	}
}

func TestRoutinesNone(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE a (id INT PRIMARY KEY)")
	s := openSnapshot(t, cfg, db)
	got, err := s.Routines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("routines = %+v, want none", got)
	}
}
