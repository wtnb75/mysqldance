package source

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestUseReadsSameSnapshot checks that switching databases with Use keeps the
// transaction: a row committed to the second database after the snapshot was
// taken (while the first database was being read) must not be visible.
func TestUseReadsSameSnapshot(t *testing.T) {
	cfg, a, connA := testDB(t)
	_, b, connB := testDB(t)
	mustExec(t, connA, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1)")
	mustExec(t, connB, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1)")

	ctx := context.Background()
	s := openSnapshot(t, cfg, a)
	if got, want := rowsOf(t, s, "t"), [][]string{{"1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows of %s = %v, want %v", a, got, want)
	}

	// Committed on another connection after the snapshot started.
	mustExec(t, connB, "INSERT INTO t VALUES (2)")

	if err := s.Use(ctx, b); err != nil {
		t.Fatalf("Use(%s): %v", b, err)
	}
	if got, want := rowsOf(t, s, "t"), [][]string{{"1"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows of %s = %v, want %v (the row committed after the snapshot must not be visible)", b, got, want)
	}
}

// TestUseResetsPerDatabaseState checks that Use switches to the other
// database, even when both have a table of the same name.
func TestUseResetsPerDatabaseState(t *testing.T) {
	cfg, a, connA := testDB(t)
	_, b, connB := testDB(t)
	mustExec(t, connA, "CREATE TABLE t (id INT PRIMARY KEY, x INT)")
	mustExec(t, connB, "CREATE TABLE t (id INT PRIMARY KEY, y VARCHAR(5))")

	ctx := context.Background()
	s := openSnapshot(t, cfg, a)
	if got := columnNames(tableByName(t, s, "t")); !reflect.DeepEqual(got, []string{"id", "x"}) {
		t.Fatalf("columns of %s.t = %v", a, got)
	}
	if err := s.Use(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got := columnNames(tableByName(t, s, "t")); !reflect.DeepEqual(got, []string{"id", "y"}) {
		t.Errorf("columns of %s.t = %v, want [id y] (stale column catalog)", b, got)
	}
	create, err := s.CreateDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(create, "CREATE DATABASE `"+b+"`") {
		t.Errorf("CreateDatabase = %q, want database %s", create, b)
	}
}

func TestUseRejectsBadDatabase(t *testing.T) {
	cfg, a, _ := testDB(t)
	s := openSnapshot(t, cfg, a)
	ctx := context.Background()
	if err := s.Use(ctx, "mysql"); err == nil || !strings.Contains(err.Error(), "system database") {
		t.Errorf("Use(mysql) error = %v, want system database error", err)
	}
	if err := s.Use(ctx, "mysqldance_no_such_db"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Use(missing) error = %v, want not found", err)
	}
}

func columnNames(ti TableInfo) []string {
	out := make([]string, len(ti.Columns))
	for i, c := range ti.Columns {
		out[i] = c.Name
	}
	return out
}
