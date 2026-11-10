package source

import (
	"context"
	"strings"
	"testing"
)

func TestTriggers(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE a (id INT PRIMARY KEY, x INT)",
		"CREATE TABLE b (id INT PRIMARY KEY, x INT)",
		// Two triggers for the same event run in creation order, which is the
		// reverse of their name order here.
		"CREATE TRIGGER z_first BEFORE INSERT ON b FOR EACH ROW SET NEW.x = 1",
		"CREATE TRIGGER a_second BEFORE INSERT ON b FOR EACH ROW SET NEW.x = NEW.x + 1",
		"CREATE TRIGGER m_del AFTER DELETE ON a FOR EACH ROW SET @gone = OLD.id",
	)
	s := openSnapshot(t, cfg, db)

	got, err := s.Triggers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range got {
		names = append(names, tr.Table+"."+tr.Name)
	}
	if want := "a.m_del b.z_first b.a_second"; strings.Join(names, " ") != want {
		t.Fatalf("triggers = %q, want %q", strings.Join(names, " "), want)
	}
	for _, tr := range got {
		if !strings.HasPrefix(tr.Create, "CREATE ") || !strings.Contains(tr.Create, "TRIGGER `"+tr.Name+"`") {
			t.Errorf("trigger %s Create = %q", tr.Name, tr.Create)
		}
		// Names in the dumped database stay unqualified so the dump can be
		// restored under another database name.
		if strings.Contains(tr.Create, db) {
			t.Errorf("trigger %s Create names the database: %q", tr.Name, tr.Create)
		}
	}
}

func TestTriggersKeepSQLMode(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE a (id INT PRIMARY KEY, s VARCHAR(10))",
		"SET SESSION sql_mode = 'ANSI_QUOTES'",
		`CREATE TRIGGER t BEFORE INSERT ON a FOR EACH ROW SET NEW.s = "x"`,
	)
	s := openSnapshot(t, cfg, db)
	got, err := s.Triggers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].SQLMode, "ANSI_QUOTES") {
		t.Fatalf("triggers = %+v, want one created under ANSI_QUOTES", got)
	}
}

func TestTriggersNone(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE a (id INT PRIMARY KEY)")
	s := openSnapshot(t, cfg, db)
	got, err := s.Triggers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("triggers = %+v, want none", got)
	}
}
