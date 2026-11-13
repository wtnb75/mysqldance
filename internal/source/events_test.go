package source

import (
	"context"
	"strings"
	"testing"
)

func TestEvents(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE a (id INT PRIMARY KEY)",
		// Created out of name order; the schedule is far ahead so none runs.
		"CREATE EVENT z_last ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' DISABLE DO DELETE FROM a",
		"CREATE EVENT a_first ON SCHEDULE AT '2037-01-01 00:00:00' ON COMPLETION PRESERVE DISABLE DO BEGIN\n  SET @a = 1;\n  SET @b = 2;\nEND",
	)
	s := openSnapshot(t, cfg, db)

	got, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	// By name in byte order, not by the order they were created in.
	if want := "a_first|z_last"; strings.Join(names, "|") != want {
		t.Fatalf("events = %q, want %q", strings.Join(names, "|"), want)
	}
	for _, e := range got {
		if !strings.HasPrefix(e.Create, "CREATE ") || !strings.Contains(e.Create, " EVENT ") || !strings.Contains(e.Create, e.Name) {
			t.Errorf("event %s Create = %q", e.Name, e.Create)
		}
		// Names in the dumped database stay unqualified so the dump can be
		// restored under another database name.
		if strings.Contains(e.Create, db) {
			t.Errorf("event %s Create names the database: %q", e.Name, e.Create)
		}
		if e.TimeZone == "" {
			t.Errorf("event %s has no time zone", e.Name)
		}
	}
	if !strings.Contains(got[0].Create, "SET @a = 1;") {
		t.Errorf("event body not kept as written: %q", got[0].Create)
	}
}

func TestEventsKeepSQLModeAndTimeZone(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"SET SESSION sql_mode = 'ANSI_QUOTES'",
		"SET SESSION time_zone = '+09:00'",
		`CREATE EVENT e ON SCHEDULE EVERY 1 DAY STARTS '2037-01-01 00:00:00' DISABLE DO SELECT "x"`,
	)
	s := openSnapshot(t, cfg, db)
	got, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].SQLMode, "ANSI_QUOTES") || got[0].TimeZone != "+09:00" {
		t.Fatalf("events = %+v, want one created under ANSI_QUOTES and +09:00", got)
	}
}

func TestEventsNone(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE a (id INT PRIMARY KEY)")
	s := openSnapshot(t, cfg, db)
	got, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("events = %+v, want none", got)
	}
}
