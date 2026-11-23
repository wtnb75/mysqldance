package source

import (
	"context"
	"strings"
	"testing"
)

// TestHiddenObjectsAreAnError (B-110): with SELECT only, information_schema
// lists no trigger, routine or event and gives no error, so the dump would leave
// them out silently. The read must fail and name the missing privilege; with the
// privilege the same account reads the object.
func TestHiddenObjectsAreAnError(t *testing.T) {
	cases := []struct {
		kind  string
		setup []string
		// grants are tried in order until the server accepts one: the first that
		// lets the account list and read the objects. SHOW_ROUTINE is 8.0.20 and
		// later; only it lets an account read the body of another account's routine
		// on 8.x (EXECUTE lists it, but SHOW CREATE returns NULL). On 5.7 and
		// MariaDB it is SELECT on mysql.proc; 8.x has no such table, so that
		// GRANT fails there and the next one is tried.
		grants []string
		// read returns how many objects it read.
		read func(*Snapshot) (int, error)
		// mention is what the error must name.
		mention []string
	}{
		{
			kind: "trigger",
			setup: []string{
				"CREATE TABLE t (id INT PRIMARY KEY)",
				"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.id = NEW.id",
			},
			grants: []string{"TRIGGER ON `@DB@`.*"},
			read: func(s *Snapshot) (int, error) {
				l, err := s.Triggers(context.Background())
				return len(l), err
			},
			mention: []string{"triggers", "TRIGGER"},
		},
		{
			kind:   "routine",
			setup:  []string{"CREATE PROCEDURE p() SELECT 1"},
			grants: []string{"SHOW_ROUTINE ON *.*", "SELECT ON mysql.proc", "EXECUTE ON `@DB@`.*"},
			read: func(s *Snapshot) (int, error) {
				l, err := s.Routines(context.Background())
				return len(l), err
			},
			mention: []string{"routines", "SHOW_ROUTINE", "EXECUTE"},
		},
		{
			kind:   "event",
			setup:  []string{"CREATE EVENT ev ON SCHEDULE EVERY 1 DAY DO SELECT 1"},
			grants: []string{"EVENT ON `@DB@`.*"},
			read: func(s *Snapshot) (int, error) {
				l, err := s.Events(context.Background())
				return len(l), err
			},
			mention: []string{"events", "EVENT"},
		},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			cfg, db, conn := testDB(t)
			mustExec(t, conn, c.setup...)
			user := userWithRights(t, conn, db, false)
			cfg.User, cfg.Password, cfg.NoLock = user, "pw", true

			s := openSnapshot(t, cfg, db)
			n, err := c.read(s)
			if err == nil {
				t.Fatalf("read %d %ss without the privilege and no error", n, c.kind)
			}
			for _, want := range append(c.mention, "`"+db+"`") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %s", err, want)
				}
			}

			// With the privilege on the database, the same account reads it.
			granted := ""
			for _, g := range c.grants {
				g = strings.ReplaceAll(g, "@DB@", db)
				if _, err := conn.ExecContext(context.Background(), "GRANT "+g+" TO '"+user+"'@'%'"); err == nil {
					granted = g
					break
				}
			}
			if granted == "" {
				t.Fatalf("the server accepted none of %q", c.grants)
			}
			s = openSnapshot(t, cfg, db)
			if n, err := c.read(s); err != nil || n != 1 {
				t.Errorf("read with GRANT %s = %d objects, %v; want 1, nil", granted, n, err)
			}
		})
	}
}

// TestGlobalSelectAndRoutines: where routines live in the table mysql.proc
// (MySQL 5.7, MariaDB), SELECT on *.* lists them and reads their bodies, so a
// read-only account is not refused; on MySQL 8.x it does not, and is.
func TestGlobalSelectAndRoutines(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE PROCEDURE p() SELECT 1")
	var n int
	if err := conn.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'mysql' AND TABLE_NAME = 'proc'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	inProcTable := n > 0

	user := testUser("mdg_", db)
	mustExec(t, conn,
		"CREATE USER '"+user+"'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON *.* TO '"+user+"'@'%'",
	)
	t.Cleanup(func() { mustExec(t, conn, "DROP USER '"+user+"'@'%'") })
	cfg.User, cfg.Password, cfg.NoLock = user, "pw", true

	l, err := openSnapshot(t, cfg, db).Routines(context.Background())
	if inProcTable {
		if err != nil || len(l) != 1 {
			t.Errorf("Routines with SELECT on *.* = %d, %v; want 1, nil", len(l), err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "may not list the routines") {
		t.Errorf("Routines with SELECT on *.* = %d, %v; want the listing error", len(l), err)
	}
}

// TestPrivilegesThroughARole: an account whose TRIGGER, EVENT and EXECUTE come
// only from an active role must not be refused, whether its SHOW GRANTS folds
// the role in (MySQL 8.x) or leaves it out (MariaDB).
func TestPrivilegesThroughARole(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE t (id INT PRIMARY KEY)",
		"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.id = NEW.id",
		"CREATE PROCEDURE p() SELECT 1",
		"CREATE EVENT ev ON SCHEDULE EVERY 1 DAY DO SELECT 1",
	)
	role := testUser("mdr_", db)
	user := testUser("mdu_", db)
	if _, err := conn.ExecContext(context.Background(), "CREATE ROLE '"+role+"'"); err != nil {
		t.Skipf("the server has no roles: %v", err)
	}
	t.Cleanup(func() { mustExec(t, conn, "DROP ROLE '"+role+"'") })
	mustExec(t, conn,
		"GRANT TRIGGER, EVENT, EXECUTE ON *.* TO '"+role+"'",
		"CREATE USER '"+user+"'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON `"+db+"`.* TO '"+user+"'@'%'",
		"GRANT '"+role+"' TO '"+user+"'@'%'",
	)
	t.Cleanup(func() { mustExec(t, conn, "DROP USER '"+user+"'@'%'") })
	// MySQL says TO, MariaDB says FOR.
	if _, err := conn.ExecContext(context.Background(), "SET DEFAULT ROLE '"+role+"' TO '"+user+"'@'%'"); err != nil {
		mustExec(t, conn, "SET DEFAULT ROLE '"+role+"' FOR '"+user+"'@'%'")
	}
	cfg.User, cfg.Password, cfg.NoLock = user, "pw", true

	s := openSnapshot(t, cfg, db)
	if l, err := s.Triggers(context.Background()); err != nil || len(l) != 1 {
		t.Errorf("Triggers = %d, %v; want 1, nil", len(l), err)
	}
	if l, err := s.Events(context.Background()); err != nil || len(l) != 1 {
		t.Errorf("Events = %d, %v; want 1, nil", len(l), err)
	}
	// EXECUTE lists the routine; its body needs SHOW_ROUTINE, so only the
	// listing check is asked of Routines here.
	if _, err := s.Routines(context.Background()); err != nil && strings.Contains(err.Error(), "may not list") {
		t.Errorf("Routines refused an account with EXECUTE through a role: %v", err)
	}
}

// TestFullPrivilegesListEverything: the administrator of the test server is
// not refused (no false alarm for an account that holds the privileges).
func TestFullPrivilegesListEverything(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE PROCEDURE p() SELECT 1")
	s := openSnapshot(t, cfg, db)
	if l, err := s.Routines(context.Background()); err != nil || len(l) != 1 {
		t.Errorf("Routines = %d, %v; want 1, nil", len(l), err)
	}
	if _, err := s.Triggers(context.Background()); err != nil {
		t.Errorf("Triggers: %v", err)
	}
	if _, err := s.Events(context.Background()); err != nil {
		t.Errorf("Events: %v", err)
	}
	// The calls above also pass when the check is skipped. Make sure the real
	// SHOW GRANTS of the administrator was understood, and says it holds all.
	if !s.grantsOK {
		t.Fatalf("SHOW GRANTS of the test account was not understood; the check was skipped")
	}
	if !s.grants.covers(db, "TRIGGER") || !s.grants.covers(db, "EVENT") || !s.grants.covers(db, "EXECUTE") {
		t.Errorf("the parsed grants %+v do not cover the administrator", s.grants)
	}
}
