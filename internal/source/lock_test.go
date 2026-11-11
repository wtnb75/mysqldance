package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

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
	if w := warningsOf(t, s); len(w) != 0 {
		t.Errorf("Warnings = %q, want none while the lock keeps the tables consistent", w)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if writeBlocked(t, conn, "m") {
		t.Error("write to the MyISAM table is still blocked after Close")
	}
}

// TestLockTimeout runs a long statement on a non-InnoDB table and opens a
// snapshot that needs the global read lock: FLUSH TABLES WITH READ LOCK has to
// wait for the statement, so it must give up after Config.LockTimeout and must
// not stay queued, where it would hold up every statement behind it.
func TestLockTimeout(t *testing.T) {
	for _, jobs := range []int{1, 2} {
		t.Run(fmt.Sprintf("jobs=%d", jobs), func(t *testing.T) {
			cfg, db, conn := testDB(t)
			_, dsn := testConfig(t)
			mustExec(t, conn,
				"CREATE TABLE i (id INT PRIMARY KEY)",
				"CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM",
				"INSERT INTO m VALUES (1)",
			)

			long := pinnedConn(t, dsn)
			longDone := make(chan struct{})
			go func() {
				defer close(longDone)
				if _, err := long.ExecContext(context.Background(), "SELECT COUNT(*), SLEEP(6) FROM `"+db+"`.m"); err != nil {
					t.Errorf("long statement: %v", err)
				}
			}()
			t.Cleanup(func() { <-longDone })
			waitFor(t, conn, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE INFO LIKE 'SELECT COUNT(*), SLEEP(6)%'")

			// An account of its own, so that its sessions can be told from the ones
			// other tests open on the same server.
			user := fmt.Sprintf("mdance_%s_%d", strings.TrimPrefix(db, "mysqldance_test_"), jobs)
			mustExec(t, conn,
				"CREATE USER '"+user+"'@'%' IDENTIFIED BY 'pw'",
				"GRANT SELECT ON `"+db+"`.* TO '"+user+"'@'%'",
				"GRANT RELOAD ON *.* TO '"+user+"'@'%'",
			)
			t.Cleanup(func() { mustExec(t, conn, "DROP USER '"+user+"'@'%'") })
			cfg.User, cfg.Password = user, "pw"
			cfg.Jobs = jobs
			cfg.LockTimeout = time.Second
			start := time.Now()
			s, err := Open(context.Background(), cfg, db)
			waited := time.Since(start)
			if err == nil {
				_ = s.Close()
				t.Fatal("Open succeeded although the lock could not be taken within the timeout")
			}
			if waited > 4*time.Second {
				t.Errorf("Open took %v, want about the 1 s timeout, not the 6 s statement", waited)
			}
			var me *mysql.MySQLError
			if !errors.As(err, &me) || me.Number != errLockWaitTimeout {
				t.Errorf("error %q does not wrap MySQL error %d", err, errLockWaitTimeout)
			}
			escape := map[int]string{1: "--no-lock", 2: "--jobs 1"}[jobs]
			for _, want := range []string{"--lock-timeout", escape} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %s", err, want)
				}
			}
			// The statement is still running, and the lock request must be gone: no
			// session of this test's account is left on the server, queued or not. (A
			// write probe would also see the global locks other tests take on the same
			// server, so it cannot tell the two apart.)
			var left int
			q := "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER = ?"
			if err := conn.QueryRowContext(context.Background(), q, user).Scan(&left); err != nil {
				t.Fatal(err)
			}
			if left != 0 {
				t.Errorf("%d session(s) of %s are left on the server after the failed Open", left, user)
			}
		})
	}
}

// A zero LockTimeout leaves the server's lock_wait_timeout alone.
func TestLockTimeoutZeroKeepsServerSetting(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	cfg.LockTimeout = 0
	s := openSnapshot(t, cfg, db)
	var got, want int
	if err := s.conn.QueryRowContext(context.Background(), "SELECT @@session.lock_wait_timeout").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(context.Background(), "SELECT @@global.lock_wait_timeout").Scan(&want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("session lock_wait_timeout = %d, want the server's %d", got, want)
	}
}

func TestLockWaitSeconds(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want int64
	}{
		{time.Nanosecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{30 * time.Second, 30},
		{time.Duration(math.MaxInt64), maxLockWaitSeconds}, // must not overflow into a tiny value
		{366 * 24 * time.Hour, maxLockWaitSeconds},
	} {
		if got := lockWaitSeconds(tt.in); got != tt.want {
			t.Errorf("lockWaitSeconds(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestOpenRejectsNegativeLockTimeout(t *testing.T) {
	_, err := Open(context.Background(), Config{LockTimeout: -time.Second}, "db")
	if err == nil || !strings.Contains(err.Error(), "LockTimeout") {
		t.Errorf("Open with a negative LockTimeout: err = %v, want an error naming LockTimeout", err)
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
	w := warningsOf(t, s)
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

	_, _, err := s.Tables(context.Background())
	if err == nil {
		t.Fatal("Tables succeeded with an unlocked non-InnoDB table")
	}
	for _, want := range []string{"`late`", "MyISAM", "--no-lock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
