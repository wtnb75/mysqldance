package source

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// errLockWaitTimeout is MySQL error 1205, returned when a write waits for a
// global read lock for longer than lock_wait_timeout.
const errLockWaitTimeout = 1205

// writeBlocked reports whether an INSERT into table on conn is held up by a
// lock. conn must have lock_wait_timeout set low (see fastLockTimeout).
func writeBlocked(t *testing.T, conn *sql.Conn, table string) bool {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), "INSERT INTO "+table+" VALUES (100)")
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == errLockWaitTimeout {
		return true
	}
	t.Fatalf("INSERT INTO %s: %v", table, err)
	return false
}

// fastLockTimeout makes writes that hit a lock fail after one second.
func fastLockTimeout(t *testing.T, conn *sql.Conn) {
	t.Helper()
	mustExec(t, conn, "SET SESSION lock_wait_timeout = 1")
}

func TestLockHeldForNonInnoDB(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE i (id INT PRIMARY KEY)",
		"CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM",
		"INSERT INTO m VALUES (1)",
	)
	fastLockTimeout(t, conn)

	s, err := Open(context.Background(), cfg, db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !writeBlocked(t, conn, "m") {
		t.Error("write to the MyISAM table was not blocked while the snapshot is open")
	}
	if !writeBlocked(t, conn, "i") {
		t.Error("write to the InnoDB table was not blocked by the global read lock")
	}
	if got, want := len(rowsOf(t, s, "m")), 1; got != want {
		t.Errorf("MyISAM rows = %d, want %d", got, want)
	}
	if w := s.Warnings(); len(w) != 0 {
		t.Errorf("Warnings = %q, want none while the lock keeps the tables consistent", w)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if writeBlocked(t, conn, "m") {
		t.Error("write to the MyISAM table is still blocked after Close")
	}
}

func TestLockCoversEveryDatabase(t *testing.T) {
	cfg, db1, conn1 := testDB(t)
	_, db2, conn2 := testDB(t)
	mustExec(t, conn1, "CREATE TABLE i (id INT PRIMARY KEY)")
	mustExec(t, conn2, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	fastLockTimeout(t, conn1)

	s, err := Open(context.Background(), cfg, db1, db2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Only the second database has a non-InnoDB table, but the lock is taken
	// up front, so it also fixes the first one.
	if !writeBlocked(t, conn1, "i") {
		t.Error("write to the first database was not blocked")
	}
}

func TestNoLockForInnoDBOnly(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY)", "INSERT INTO i VALUES (1)")
	fastLockTimeout(t, conn)

	s := openSnapshot(t, cfg, db)
	if writeBlocked(t, conn, "i") {
		t.Error("write was blocked although every table is InnoDB")
	}
	// The snapshot itself still does not see the new row.
	if got, want := len(rowsOf(t, s, "i")), 1; got != want {
		t.Errorf("rows = %d, want %d", got, want)
	}
}

func TestNoLockOption(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	fastLockTimeout(t, conn)

	cfg.NoLock = true
	s := openSnapshot(t, cfg, db)
	if writeBlocked(t, conn, "m") {
		t.Error("write was blocked although NoLock is set")
	}
	_ = tableByName(t, s, "m")
	w := s.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "`m`") || !strings.Contains(w[0], "MyISAM") {
		t.Errorf("Warnings = %q, want one warning about `m` (MyISAM)", w)
	}
}

func TestLockNeedsReloadPrivilege(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	user := "mdance_" + strings.TrimPrefix(db, "mysqldance_test_")
	mustExec(t, conn,
		"CREATE USER '"+user+"'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON `"+db+"`.* TO '"+user+"'@'%'",
	)
	t.Cleanup(func() { mustExec(t, conn, "DROP USER '"+user+"'@'%'") })

	cfg.User, cfg.Password = user, "pw"
	_, err := Open(context.Background(), cfg, db)
	if err == nil {
		t.Fatal("Open succeeded without the RELOAD privilege")
	}
	for _, want := range []string{"RELOAD", "--no-lock", "`m`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}

	// With --no-lock the same account works.
	cfg.NoLock = true
	openSnapshot(t, cfg, db)
}

// TestNonInnoDBTableWithoutLock covers a non-InnoDB table read although no
// lock is held and NoLock is not set (Open found nothing to lock, so the table
// must have appeared later): the dump must fail rather than silently be
// inconsistent. MySQL 8 reads the data dictionary under the snapshot, so the
// state is forced here instead of raced.
func TestNonInnoDBTableWithoutLock(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE late (id INT PRIMARY KEY) ENGINE=MyISAM")
	cfg.NoLock = true
	s := openSnapshot(t, cfg, db)
	s.noLock = false

	_, err := s.Tables(context.Background())
	if err == nil {
		t.Fatal("Tables succeeded with an unlocked non-InnoDB table")
	}
	for _, want := range []string{"`late`", "MyISAM", "--no-lock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
