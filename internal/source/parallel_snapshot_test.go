package source

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// These tests verify, on a real server, the approach for aligning several
// connections on one point in time (design doc, section 6): take
// FLUSH TABLES WITH READ LOCK on one connection, start a consistent snapshot on
// every other connection, then release (or keep) the lock. Nothing here is used
// by production code yet; the parallel read itself is a separate backlog item.

// errAccessDenied is MySQL error 1227 (ER_SPECIFIC_ACCESS_DENIED_ERROR), the
// answer to FLUSH TABLES WITH READ LOCK without the RELOAD privilege.
const errAccessDenied = 1227

// testPool opens a pool on dsn that is closed at cleanup.
func testPool(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	pool, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// pinnedConn takes one connection from a new pool on dsn; it is closed at cleanup.
func pinnedConn(t *testing.T, dsn string) *sql.Conn {
	t.Helper()
	c, err := testPool(t, dsn).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// snapshotConns opens n pinned connections on db, each in a REPEATABLE READ
// consistent snapshot, with a pause between them so that a writer which is not
// held back would commit in between.
func snapshotConns(t *testing.T, dsn, db string, n int, pause time.Duration) []*sql.Conn {
	t.Helper()
	pool := testPool(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var conns []*sql.Conn
	for range n {
		c, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		mustExec(t, c,
			"USE `"+db+"`",
			"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
			"START TRANSACTION WITH CONSISTENT SNAPSHOT",
		)
		conns = append(conns, c)
		time.Sleep(pause)
	}
	return conns
}

func countRows(t *testing.T, c *sql.Conn, table string) int {
	t.Helper()
	var n int
	if err := c.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// waitFor polls query (which returns one count) until it is positive, and fails
// the test after 10 seconds.
func waitFor(t *testing.T, c *sql.Conn, query string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := c.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached within 10s: %s", query)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestParallelSnapshotAlignedByGlobalLock shows that with the global read lock
// held while the snapshots start, every connection sees the same InnoDB
// contents even though a writer is trying to commit all the time, and that the
// lock must stay held for a non-InnoDB table (a snapshot does not cover it).
func TestParallelSnapshotAlignedByGlobalLock(t *testing.T) {
	_, db, conn := testDB(t)
	_, dsn := testConfig(t)
	mustExec(t, conn,
		"CREATE TABLE i (id INT PRIMARY KEY)",
		"CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM",
		"INSERT INTO i VALUES (1), (2), (3)",
		"INSERT INTO m VALUES (1), (2), (3)",
	)
	pool := testPool(t, dsn)
	ctx := context.Background()
	locker := pinnedConn(t, dsn)

	lockStart := time.Now()
	mustExec(t, locker, "FLUSH TABLES WITH READ LOCK")

	// A writer that tries to commit to both tables as soon as the lock is held.
	writerDone := make(chan error, 1)
	go func() {
		w, err := pool.Conn(ctx)
		if err != nil {
			writerDone <- err
			return
		}
		defer func() { _ = w.Close() }()
		for _, q := range []string{
			"INSERT INTO `" + db + "`.i VALUES (100)",
			"INSERT INTO `" + db + "`.m VALUES (100)",
		} {
			if _, err := w.ExecContext(ctx, q); err != nil {
				writerDone <- err
				return
			}
		}
		writerDone <- nil
	}()

	const n = 4
	snapStart := time.Now()
	conns := snapshotConns(t, dsn, db, n, 100*time.Millisecond)
	t.Logf("%d snapshots started in %v while the global read lock was held", n, time.Since(snapStart))

	// The writer is queued behind the lock, not merely slow to start.
	waitFor(t, conn, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE STATE = 'Waiting for global read lock'")
	select {
	case err := <-writerDone:
		t.Fatalf("writer finished while the global read lock was held (err=%v)", err)
	default:
	}

	// While the lock is held, every connection sees the same state of both
	// the InnoDB and the MyISAM table.
	for k, c := range conns {
		if got := countRows(t, c, "i"); got != 3 {
			t.Errorf("conn %d: i has %d rows under the lock, want 3", k, got)
		}
		if got := countRows(t, c, "m"); got != 3 {
			t.Errorf("conn %d: m has %d rows under the lock, want 3", k, got)
		}
	}

	mustExec(t, locker, "UNLOCK TABLES")
	t.Logf("global read lock held for %v (including the snapshots above)", time.Since(lockStart))
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("writer: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer was not released by UNLOCK TABLES")
	}

	// After the lock is gone the InnoDB snapshots still agree; the MyISAM
	// table is no longer covered, so its rows follow the writer.
	for k, c := range conns {
		if got := countRows(t, c, "i"); got != 3 {
			t.Errorf("conn %d: i has %d rows after UNLOCK, want 3 (snapshot)", k, got)
		}
		if got := countRows(t, c, "m"); got != 4 {
			t.Errorf("conn %d: m has %d rows after UNLOCK, want 4 (MyISAM is not covered by the snapshot)", k, got)
		}
	}
}

// TestParallelSnapshotWithoutLockDiffers is the control for the test above:
// without the global read lock, connections that start their snapshots one
// after another see different contents when a writer keeps committing.
func TestParallelSnapshotWithoutLockDiffers(t *testing.T) {
	_, db, conn := testDB(t)
	_, dsn := testConfig(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY AUTO_INCREMENT, v INT)", "INSERT INTO i (v) VALUES (0)")

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := conn.ExecContext(context.Background(), "INSERT INTO i (v) VALUES (1)"); err != nil {
				t.Errorf("writer: %v", err)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	// Stop the writer before the cleanup drops the database, also when
	// snapshotConns fails the test.
	defer func() {
		close(stop)
		<-done
	}()
	conns := snapshotConns(t, dsn, db, 3, 100*time.Millisecond)

	seen := map[int]bool{}
	for _, c := range conns {
		seen[countRows(t, c, "i")] = true
	}
	if len(seen) < 2 {
		t.Errorf("all snapshots saw the same row count %v without a lock; the control shows nothing", seen)
	}
}

// TestParallelSnapshotLockWaitsForRunningStatement measures how long the lock takes to
// be granted while a long statement is running: that wait is added to the time
// writers are stalled, and a statement arriving behind the lock waits as well.
func TestParallelSnapshotLockWaitsForRunningStatement(t *testing.T) {
	_, db, conn := testDB(t)
	_, dsn := testConfig(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY)", "INSERT INTO i VALUES (1)")
	ctx := context.Background()

	long := pinnedConn(t, dsn)
	longDone := make(chan struct{})
	go func() {
		defer close(longDone)
		// A statement that holds a read lock on the table for a while.
		if _, err := long.ExecContext(ctx, "SELECT COUNT(*), SLEEP(2) FROM `"+db+"`.i"); err != nil {
			t.Errorf("long statement: %v", err)
		}
	}()
	// Wait until the server is running it, instead of guessing with a sleep.
	waitFor(t, conn, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE INFO LIKE 'SELECT COUNT(*), SLEEP(2)%'")

	locker := pinnedConn(t, dsn)
	start := time.Now()
	mustExec(t, locker, "FLUSH TABLES WITH READ LOCK")
	waited := time.Since(start)
	mustExec(t, locker, "UNLOCK TABLES")
	<-longDone
	t.Logf("FLUSH TABLES WITH READ LOCK waited %v for a running 2 s statement", waited)
	if waited < 500*time.Millisecond {
		t.Errorf("FLUSH TABLES WITH READ LOCK took %v; it should wait for the running statement", waited)
	}
}

// TestParallelSnapshotPrivileges records which privileges each step needs: the
// global read lock needs RELOAD; starting a consistent snapshot and reading
// need only SELECT.
func TestParallelSnapshotPrivileges(t *testing.T) {
	_, db, conn := testDB(t)
	_, rootDSN := testConfig(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY)", "INSERT INTO i VALUES (1)")
	suffix := strings.TrimPrefix(db, "mysqldance_test_")
	selectOnly, withReload := "mds_"+suffix, "mdr_"+suffix
	mustExec(t, conn,
		"CREATE USER '"+selectOnly+"'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON `"+db+"`.* TO '"+selectOnly+"'@'%'",
		"CREATE USER '"+withReload+"'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON `"+db+"`.* TO '"+withReload+"'@'%'",
		"GRANT RELOAD ON *.* TO '"+withReload+"'@'%'",
	)
	t.Cleanup(func() {
		mustExec(t, conn, "DROP USER '"+selectOnly+"'@'%'", "DROP USER '"+withReload+"'@'%'")
	})

	dsnFor := func(user string) string {
		mc, err := mysql.ParseDSN(rootDSN)
		if err != nil {
			t.Fatal(err)
		}
		mc.User, mc.Passwd = user, "pw"
		return mc.FormatDSN()
	}

	// SELECT only: the snapshots and the reads work, the lock does not.
	conns := snapshotConns(t, dsnFor(selectOnly), db, 2, 0)
	for k, c := range conns {
		if got := countRows(t, c, "i"); got != 1 {
			t.Errorf("SELECT-only conn %d: %d rows, want 1", k, got)
		}
	}
	_, err := conns[0].ExecContext(context.Background(), "FLUSH TABLES WITH READ LOCK")
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != errAccessDenied {
		t.Errorf("FLUSH TABLES WITH READ LOCK without RELOAD: err = %v, want error %d", err, errAccessDenied)
	}

	// SELECT + RELOAD is enough for the whole approach.
	locker := pinnedConn(t, dsnFor(withReload))
	mustExec(t, locker, "FLUSH TABLES WITH READ LOCK")
	conns = snapshotConns(t, dsnFor(withReload), db, 2, 0)
	mustExec(t, locker, "UNLOCK TABLES")
	for k, c := range conns {
		if got := countRows(t, c, "i"); got != 1 {
			t.Errorf("SELECT+RELOAD conn %d: %d rows, want 1", k, got)
		}
	}
}
