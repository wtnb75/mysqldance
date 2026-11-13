package source

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests of Config.Jobs: several connections reading one database at the same
// point in time (design doc, section 6.1).

func TestJobsRejectedBeforeConnecting(t *testing.T) {
	// Open checks the options first, so no server is needed.
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"negative", Config{Jobs: -1}, "Jobs"},
		{"too many", Config{Jobs: MaxJobs + 1}, "Jobs"},
		{"with NoLock", Config{Jobs: 2, NoLock: true}, "NoLock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Open(context.Background(), tc.cfg, "x")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Open = %v, want an error mentioning %s", err, tc.want)
			}
		})
	}
}

func TestJobsReaderCount(t *testing.T) {
	cfg, db, _ := testDB(t)
	for jobs, want := range map[int]int{0: 1, 1: 1, 2: 2, 5: 5} {
		cfg.Jobs = jobs
		s := openSnapshot(t, cfg, db)
		if got := len(s.Readers()); got != want {
			t.Errorf("Jobs=%d: %d readers, want %d", jobs, got, want)
		}
		if s.Readers()[0] != s {
			t.Errorf("Jobs=%d: the first reader is not the snapshot itself", jobs)
		}
	}
}

// TestJobsTablesAndWarningsSameAsSerial: the table list and its warnings are
// read on the first connection and follow table name order, whatever Jobs is.
func TestJobsTablesAndWarningsSameAsSerial(t *testing.T) {
	t.Parallel()
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE c (n INT)",
		"CREATE TABLE a (n INT)",
		"CREATE TABLE b (n INT)",
	)
	// TABLE_ROWS of a new table is usually 0, so lower the threshold below it
	// (as TestWarningsLargeTable does) to get a warning for every table.
	cfg.LargeTableRows = -1

	tablesAndWarnings := func(jobs int) ([]TableInfo, []string) {
		cfg.Jobs = jobs
		tables, warnings, err := openSnapshot(t, cfg, db).Tables(context.Background())
		if err != nil {
			t.Fatalf("Tables(jobs=%d): %v", jobs, err)
		}
		return tables, warnings
	}
	wantTables, wantWarnings := tablesAndWarnings(1)
	if len(wantWarnings) != 3 {
		t.Fatalf("serial warnings = %q, want one per table", wantWarnings)
	}
	for _, jobs := range []int{2, 8} {
		tables, warnings := tablesAndWarnings(jobs)
		if !reflect.DeepEqual(tables, wantTables) {
			t.Errorf("Jobs=%d: tables = %v, want %v", jobs, tables, wantTables)
		}
		if !reflect.DeepEqual(warnings, wantWarnings) {
			t.Errorf("Jobs=%d: warnings = %q, want %q", jobs, warnings, wantWarnings)
		}
	}
}

// TestJobsReadersPinned checks that every reader has the pinned session
// settings, not only the first.
func TestJobsReadersPinned(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY)")
	cfg.Jobs = 3
	s := openSnapshot(t, cfg, db)
	for k, r := range s.Readers() {
		var tz, mode string
		if err := r.conn.QueryRowContext(context.Background(),
			"SELECT @@session.time_zone, @@session.sql_mode").Scan(&tz, &mode); err != nil {
			t.Fatal(err)
		}
		if tz != "+00:00" || mode != "" {
			t.Errorf("reader %d: time_zone=%q sql_mode=%q, want '+00:00' and ''", k, tz, mode)
		}
	}
}

// TestJobsSnapshotsAligned opens the readers while a writer keeps committing a
// row into each of two InnoDB tables in one transaction: every reader must see
// the same, whole transactions only.
func TestJobsSnapshotsAligned(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE a (id INT PRIMARY KEY AUTO_INCREMENT, v INT)",
		"CREATE TABLE b (id INT PRIMARY KEY AUTO_INCREMENT, v INT)",
		"INSERT INTO a (v) VALUES (0)", "INSERT INTO b (v) VALUES (0)",
	)
	_, dsn := testConfig(t)
	pool := testPool(t, dsn)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		ctx := context.Background()
		w, err := pool.Conn(ctx)
		if err != nil {
			t.Errorf("writer: %v", err)
			return
		}
		defer func() { _ = w.Close() }()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, q := range []string{
				"BEGIN",
				"INSERT INTO `" + db + "`.a (v) VALUES (1)",
				"INSERT INTO `" + db + "`.b (v) VALUES (1)",
				"COMMIT",
			} {
				if _, err := w.ExecContext(ctx, q); err != nil {
					t.Errorf("writer %s: %v", q, err)
					return
				}
			}
		}
	})
	t.Cleanup(func() { close(stop); wg.Wait() })

	// Several rounds, so a missing lock would show up with some probability.
	cfg.Jobs = 4
	for round := range 5 {
		s, err := Open(context.Background(), cfg, db)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		var counts []int
		for k, r := range s.Readers() {
			for _, table := range []string{"a", "b"} {
				var n int
				if err := r.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM `"+db+"`."+table).Scan(&n); err != nil {
					t.Fatalf("round %d reader %d: %v", round, k, err)
				}
				counts = append(counts, n)
			}
		}
		for i, n := range counts {
			if n != counts[0] {
				t.Errorf("round %d: counts of a and b over the readers = %v, want all equal (entry %d differs)", round, counts, i)
				break
			}
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// TestJobsLockReleasedForInnoDB: when every table is InnoDB the global read
// lock is released as soon as the snapshots have started.
func TestJobsLockReleasedForInnoDB(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY)", "INSERT INTO i VALUES (1)")
	fastLockTimeout(t, conn)

	cfg.Jobs = 3
	s := openSnapshot(t, cfg, db)
	if writeBlocked(t, conn, "i") {
		t.Error("write was blocked although every table is InnoDB")
	}
	for k, r := range s.Readers() {
		rows, err := r.Rows(context.Background(), tableByName(t, r, "i"))
		if err != nil {
			t.Fatalf("reader %d: %v", k, err)
		}
		if len(rows) != 1 {
			t.Errorf("reader %d: %d rows, want 1 (the snapshot does not see the new row)", k, len(rows))
		}
	}
}

// TestJobsLockHeldForNonInnoDB: a non-InnoDB table is not covered by the
// snapshots, so the lock stays until Close and every reader reads the same.
func TestJobsLockHeldForNonInnoDB(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE i (id INT PRIMARY KEY)",
		"CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM",
		"INSERT INTO m VALUES (1)",
	)
	fastLockTimeout(t, conn)

	cfg.Jobs = 3
	s, err := Open(context.Background(), cfg, db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !writeBlocked(t, conn, "m") {
		t.Error("write to the MyISAM table was not blocked while the snapshot is open")
	}
	for k, r := range s.Readers() {
		tables, warnings, err := r.Tables(context.Background())
		if err != nil {
			t.Fatalf("reader %d: Tables: %v", k, err)
		}
		if len(warnings) != 0 {
			t.Errorf("reader %d: warnings %q, want none while the lock is held", k, warnings)
		}
		for _, ti := range tables {
			if ti.Name != "m" {
				continue
			}
			rows, err := r.Rows(context.Background(), ti)
			if err != nil || len(rows) != 1 {
				t.Errorf("reader %d: Rows(m) = %d rows, %v; want 1 row", k, len(rows), err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if writeBlocked(t, conn, "m") {
		t.Error("write is still blocked after Close")
	}
}

// userWithRights creates an account that has SELECT on db (and RELOAD when
// reload is set) and returns its name; the account is dropped at cleanup.
func userWithRights(t *testing.T, admin *sql.Conn, db string, reload bool) string {
	t.Helper()
	user := testUser("mdj_", db)
	if reload {
		user = testUser("mdjr_", db)
	}
	mustExec(t, admin,
		"CREATE USER '"+user+"'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON `"+db+"`.* TO '"+user+"'@'%'",
	)
	if reload {
		mustExec(t, admin, "GRANT RELOAD ON *.* TO '"+user+"'@'%'")
	}
	t.Cleanup(func() { mustExec(t, admin, "DROP USER '"+user+"'@'%'") })
	return user
}

// sessionsOf counts the server sessions of user.
func sessionsOf(t *testing.T, admin *sql.Conn, user string) int {
	t.Helper()
	var n int
	if err := admin.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER = ?", user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitNoSessions fails the test unless user has no session within 10 seconds.
func waitNoSessions(t *testing.T, admin *sql.Conn, user string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		n := sessionsOf(t, admin, user)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions of %s are still open 10s later", n, user)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestJobsNeedsReloadPrivilege(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE i (id INT PRIMARY KEY)")
	user := userWithRights(t, conn, db, false)

	cfg.User, cfg.Password = user, "pw"
	// One connection needs no lock when every table is InnoDB.
	single, err := Open(context.Background(), cfg, db)
	if err != nil {
		t.Fatalf("Open with one job: %v", err)
	}
	if err := single.Close(); err != nil {
		t.Fatal(err)
	}

	cfg.Jobs = 2
	_, err = Open(context.Background(), cfg, db)
	if err == nil {
		t.Fatal("Open with Jobs=2 succeeded without the RELOAD privilege")
	}
	for _, want := range []string{"RELOAD", "--jobs 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
	// The failed Open must not leave sessions behind.
	waitNoSessions(t, conn, user)
}

// TestJobsCloseEndsEverySession: Close ends the transactions of all readers
// and the lock connection, so no session of the account is left and the lock
// is gone.
func TestJobsCloseEndsEverySession(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	fastLockTimeout(t, conn)
	user := userWithRights(t, conn, db, true)

	cfg.User, cfg.Password, cfg.Jobs = user, "pw", 3
	s, err := Open(context.Background(), cfg, db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Three readers and the connection holding the lock.
	if got := sessionsOf(t, conn, user); got != 4 {
		t.Errorf("%d sessions while open, want 4", got)
	}
	if !writeBlocked(t, conn, "m") {
		t.Error("write was not blocked while open")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitNoSessions(t, conn, user)
	if writeBlocked(t, conn, "m") {
		t.Error("write is still blocked after Close")
	}
}

// TestJobsFailedOpenEndsEverySession: a failure after the lock was taken and
// some readers started (here an unknown database) closes them all.
func TestJobsFailedOpenEndsEverySession(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	fastLockTimeout(t, conn)
	user := userWithRights(t, conn, db, true)

	cfg.User, cfg.Password, cfg.Jobs = user, "pw", 3
	// The missing first database is found by Use, after the lock was taken (the
	// MyISAM table of the second one makes Open keep it) and the snapshots
	// were started.
	if _, err := Open(context.Background(), cfg, db+"_missing", db); err == nil {
		t.Fatal("Open of a missing database succeeded")
	}
	waitNoSessions(t, conn, user)
	if writeBlocked(t, conn, "m") {
		t.Error("write is blocked after a failed Open")
	}
}

// TestJobsUseSwitchesEveryReader: after Use, every reader reads the new
// database.
func TestJobsUseSwitchesEveryReader(t *testing.T) {
	cfg, db1, conn1 := testDB(t)
	_, db2, conn2 := testDB(t)
	mustExec(t, conn1, "CREATE TABLE one (id INT PRIMARY KEY)", "INSERT INTO one VALUES (1)")
	mustExec(t, conn2, "CREATE TABLE two (id INT PRIMARY KEY)", "INSERT INTO two VALUES (2), (3)")

	cfg.Jobs = 3
	s, err := Open(context.Background(), cfg, db1, db2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Use(context.Background(), db2); err != nil {
		t.Fatalf("Use: %v", err)
	}
	for k, r := range s.Readers() {
		rows, err := r.Rows(context.Background(), tableByName(t, r, "two"))
		if err != nil || len(rows) != 2 {
			t.Errorf("reader %d: Rows(two) = %d rows, %v; want 2 rows", k, len(rows), err)
		}
	}
}
