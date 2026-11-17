package source

import (
	"context"
	"strings"
	"testing"
)

func TestSplitTableSpec(t *testing.T) {
	for _, tt := range []struct {
		spec, db, table string
		ok              bool
	}{
		{"db.t", "db", "t", true},
		// The first dot splits; a table name may contain dots.
		{"db.a.b", "db", "a.b", true},
		{"", "", "", false},
		{"t", "", "", false},
		{".t", "", "", false},
		{"db.", "", "", false},
		{".", "", "", false},
	} {
		db, table, err := SplitTableSpec(tt.spec)
		if (err == nil) != tt.ok || db != tt.db || table != tt.table {
			t.Errorf("SplitTableSpec(%q) = %q, %q, %v; want %q, %q, ok=%v", tt.spec, db, table, err, tt.db, tt.table, tt.ok)
		}
	}
}

// A spec of a database that is not dumped fails before any connection is made.
func TestOpenRejectsExcludeOfOtherDatabase(t *testing.T) {
	_, err := Open(context.Background(), Config{ExcludeTables: []string{"other.t"}}, "db1")
	if err == nil || !strings.Contains(err.Error(), `"other.t"`) {
		t.Errorf("Open: err = %v, want an error naming other.t", err)
	}
	_, err = Open(context.Background(), Config{ExcludeTables: []string{"nodot"}}, "db1")
	if err == nil || !strings.Contains(err.Error(), `"nodot"`) {
		t.Errorf("Open: err = %v, want an error naming nodot", err)
	}
}

func excludeCfg(cfg Config, specs ...string) Config {
	cfg.ExcludeTables = specs
	return cfg
}

func tableNames(t *testing.T, s *Snapshot) []string {
	t.Helper()
	tables, _, err := s.Tables(context.Background())
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	var names []string
	for _, ti := range tables {
		names = append(names, ti.Name)
	}
	return names
}

func TestExcludeTableLeavesItOut(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE keep (id INT PRIMARY KEY)",
		"CREATE TABLE sessions (id INT PRIMARY KEY, v INT)",
		"CREATE TRIGGER keep_bi BEFORE INSERT ON keep FOR EACH ROW SET @x = 1",
		"CREATE TRIGGER sessions_bi BEFORE INSERT ON sessions FOR EACH ROW SET @x = 2",
	)
	s := openSnapshot(t, excludeCfg(cfg, db+".sessions"), db)
	ctx := context.Background()

	if got := tableNames(t, s); len(got) != 1 || got[0] != "keep" {
		t.Errorf("Tables = %q, want [keep]", got)
	}
	defs, err := s.Defs(ctx)
	if err != nil {
		t.Fatalf("Defs: %v", err)
	}
	if _, ok := defs["sessions"]; ok || len(defs) != 1 {
		t.Errorf("Defs has %d entries (sessions: %v), want only keep", len(defs), ok)
	}
	triggers, err := s.Triggers(ctx)
	if err != nil {
		t.Fatalf("Triggers: %v", err)
	}
	if len(triggers) != 1 || triggers[0].Name != "keep_bi" {
		t.Errorf("Triggers = %+v, want only keep_bi", triggers)
	}
}

func TestExcludeTableValidation(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE t (id INT PRIMARY KEY)", "CREATE VIEW v AS SELECT id FROM t")
	for _, tt := range []struct{ name, spec, want string }{
		{"missing table", db + ".nope", "no such table"},
		{"view", db + ".v", "not a base table"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(context.Background(), excludeCfg(cfg, tt.spec), db)
			if err == nil || !strings.Contains(err.Error(), tt.spec) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Open: err = %v, want one naming %q and containing %q", err, tt.spec, tt.want)
			}
		})
	}
}

// An excluded table still counts for the lock decision: the lock is held for a
// MyISAM table that is left out.
func TestExcludedNonInnoDBStillTakesLock(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY)", "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	fastLockTimeout(t, conn)
	s := openSnapshot(t, excludeCfg(cfg, db+".m"), db)
	if !writeBlocked(t, conn, "i") {
		t.Error("write was not blocked: the excluded MyISAM table must still make Open take the lock")
	}
	if w := warningsOf(t, s); len(w) != 0 {
		t.Errorf("warnings = %q, want none for the excluded table", w)
	}
}

func TestExcludedRefsWarnings(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE parent (id INT PRIMARY KEY)",
		"CREATE TABLE child (id INT PRIMARY KEY, pid INT, CONSTRAINT child_fk FOREIGN KEY (pid) REFERENCES parent (id))",
		"CREATE TABLE other (id INT PRIMARY KEY)",
		"CREATE VIEW v_parent AS SELECT id FROM parent",
		"CREATE VIEW v_other AS SELECT id FROM other",
	)
	s := openSnapshot(t, excludeCfg(cfg, db+".parent"), db)
	got, err := s.ExcludedRefs(context.Background())
	if err != nil {
		t.Fatalf("ExcludedRefs: %v", err)
	}
	var fk, view int
	for _, w := range got {
		switch {
		case strings.Contains(w.Message, "foreign key `child_fk` of table `child`") && strings.Contains(w.Message, "`parent`"):
			if w.Table != "child" {
				t.Errorf("foreign key warning Table = %q, want child", w.Table)
			}
			fk++
		case strings.Contains(w.Message, "view `v_parent` reads table `parent`"):
			if w.Table != "" {
				t.Errorf("view warning Table = %q, want none", w.Table)
			}
			view++
		default:
			t.Errorf("unexpected warning: %s", w.Message)
		}
	}
	if fk != 1 || view != 1 {
		t.Errorf("warnings = %q, want one for the foreign key and one for v_parent", got)
	}

	// Without an exclusion there is nothing to say.
	s2 := openSnapshot(t, cfg, db)
	if got, err := s2.ExcludedRefs(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("ExcludedRefs without exclusion = %q, %v; want none", got, err)
	}
}

// A foreign key of a table that is itself left out is not reported: nothing of
// that table is output.
func TestExcludedRefsSkipsExcludedReferrer(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE parent (id INT PRIMARY KEY)",
		"CREATE TABLE child (id INT PRIMARY KEY, pid INT, CONSTRAINT child_fk FOREIGN KEY (pid) REFERENCES parent (id))",
	)
	s := openSnapshot(t, excludeCfg(cfg, db+".parent", db+".child"), db)
	if got, err := s.ExcludedRefs(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("ExcludedRefs = %q, %v; want none", got, err)
	}
}
