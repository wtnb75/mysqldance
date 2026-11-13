package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"mysqldance/internal/extsort"
	"mysqldance/internal/sqlvalue"
)

// Config holds connection parameters. Socket, when set, takes precedence
// over Host/Port. Empty Host means 127.0.0.1 and zero Port means 3306.
type Config struct {
	Host     string
	Port     int
	Socket   string
	User     string
	Password string
	// SSLMode is one of the SSL* constants; empty means SSLPreferred.
	SSLMode string
	// SSLCA is a PEM file of CA certificates used to verify the server.
	SSLCA string
	// NoLock skips the global read lock that is otherwise taken when a
	// database has a non-InnoDB table; such tables are then read without
	// consistency (a warning is reported).
	NoLock bool
	// Jobs is the number of connections that read rows in parallel; 0 means 1.
	// Above 1, Open always takes the global read lock (it needs the RELOAD
	// privilege) to start all the snapshots at one point in time, so it cannot
	// be combined with NoLock.
	Jobs int
	// LockTimeout bounds how long FLUSH TABLES WITH READ LOCK may wait: it first
	// waits for running statements, and every statement that arrives behind it
	// waits as well. When it is exceeded Open fails. It is rounded up to whole
	// seconds. Zero leaves the server's lock_wait_timeout in force (no limit of
	// the tool's own); a negative value is an error.
	LockTimeout time.Duration
	// ConnectTimeout bounds making the first connection: the TCP connect, the
	// server's greeting and the login. Zero sets no limit of its own (the TCP
	// connect is left to the operating system); a negative value is an error.
	ConnectTimeout time.Duration
	// ReadTimeout bounds each wait for the server's response, so a server that
	// stops answering fails the dump instead of stalling it. It is not a limit
	// on the whole transfer, but it also covers a statement that sends nothing
	// until it is done (a large ORDER BY, the wait for the global read lock),
	// so a small value cuts those off. Zero means no limit; a negative value
	// is an error.
	ReadTimeout time.Duration
	// SortFile, when set, makes EachRow sort a table without a key through
	// scratch files it creates once the rows held reach about SortMemory bytes,
	// instead of holding the whole table in memory. With Jobs above 1, that many
	// tables are sorted at once.
	SortFile   extsort.NewFile
	SortMemory int64
	// LargeTableRows is the TABLE_ROWS estimate above which Tables warns about a
	// table without a key; 0 means DefaultLargeTableRows. A negative value warns
	// for every such table (a new table's estimate is 0, so tests use it).
	LargeTableRows int64
	// Log receives how the snapshot is made: the target, the connections, the
	// lock and how long it is awaited and held (Info), and the statements that
	// take the lock and start the snapshots (Debug). Nil discards them. It never
	// gets the password.
	Log *slog.Logger
}

// MaxJobs is the largest Config.Jobs: each job is a server connection.
const MaxJobs = 64

// DefaultLockTimeout is the LockTimeout the command line uses unless told
// otherwise.
const DefaultLockTimeout = 30 * time.Second

// DefaultConnectTimeout is the ConnectTimeout the command line uses unless
// told otherwise. There is no default ReadTimeout: it would cut off a normal
// read of a large table (see Config.ReadTimeout).
const DefaultConnectTimeout = 30 * time.Second

// maxLockWaitSeconds is the largest value of the lock_wait_timeout variable.
const maxLockWaitSeconds = 31536000

// errLockWaitTimeout is MySQL error 1205 (ER_LOCK_WAIT_TIMEOUT).
const errLockWaitTimeout = 1205

var systemDatabases = []string{"information_schema", "mysql", "performance_schema", "sys"}

func checkNotSystem(db string) error {
	for _, sys := range systemDatabases {
		if strings.EqualFold(db, sys) {
			return fmt.Errorf("refusing to dump system database %q", db)
		}
	}
	return nil
}

// Open connects, starts a consistent snapshot and selects the first of dbs.
// Use switches to the others within the same snapshot. dbs must list every
// database that will be read: when any of them has a non-InnoDB table, a
// global read lock is taken before the snapshot starts and held until Close
// (unless cfg.NoLock), so those tables are consistent with the rest. With
// cfg.Jobs above 1 the lock is always taken, and kept only for that reason
// (see startParallel).
func Open(ctx context.Context, cfg Config, dbs ...string) (*Snapshot, error) {
	if cfg.Jobs < 0 || cfg.Jobs > MaxJobs {
		return nil, fmt.Errorf("option Jobs must be between 1 and %d, not %d", MaxJobs, cfg.Jobs)
	}
	if cfg.Jobs > 1 && cfg.NoLock {
		// Without the lock the snapshots of the connections start one after
		// another and would not show the same point in time.
		return nil, errors.New("option Jobs above 1 cannot be combined with option NoLock: " +
			"the snapshots of the connections could not be aligned")
	}
	if cfg.LockTimeout < 0 {
		return nil, fmt.Errorf("option LockTimeout must not be negative, not %v", cfg.LockTimeout)
	}
	if cfg.ConnectTimeout < 0 {
		return nil, fmt.Errorf("option ConnectTimeout must not be negative, not %v", cfg.ConnectTimeout)
	}
	if cfg.ReadTimeout < 0 {
		return nil, fmt.Errorf("option ReadTimeout must not be negative, not %v", cfg.ReadTimeout)
	}
	if len(dbs) == 0 {
		return nil, errors.New("no database to read")
	}
	for _, db := range dbs {
		if err := checkNotSystem(db); err != nil {
			return nil, err
		}
	}

	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	host := cfg.Host
	// Only where the connection goes: never the password or the DSN.
	var target []any
	if cfg.Socket != "" {
		mc.Net, mc.Addr = "unix", cfg.Socket
		target = []any{"socket", cfg.Socket}
	} else {
		port := cfg.Port
		if host == "" {
			host = "127.0.0.1"
		}
		if port == 0 {
			port = 3306
		}
		mc.Net, mc.Addr = "tcp", net.JoinHostPort(host, strconv.Itoa(port))
		target = []any{"host", host, "port", port}
	}
	lg := cfg.Log
	if lg == nil {
		lg = slog.New(slog.DiscardHandler)
	}
	lg = lg.With(target...)
	// A bad ssl setting must fail here, as the first thing Run does, so it
	// aborts before the output directory is created or any file is deleted
	// or written.
	var err error
	if mc.TLS, mc.AllowFallbackToPlaintext, err = tlsConfig(cfg, host); err != nil {
		return nil, err
	}
	// Interpolate parameters client-side so no statement is ever prepared:
	// everything, including rows, travels over the text protocol. ParseTime
	// stays false so temporal values arrive as the server's text.
	mc.InterpolateParams = true
	mc.Timeout, mc.ReadTimeout = cfg.ConnectTimeout, cfg.ReadTimeout
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	sdb := sql.OpenDB(connector)
	err = pingWithin(ctx, sdb, cfg.ConnectTimeout)
	var s *Snapshot
	if err == nil {
		s, err = start(ctx, sdb, dbs, cfg.NoLock, cfg.Jobs, cfg.LockTimeout, lg)
	}
	if err == nil {
		// Every reader gets the settings, as with the sort ones, so that any of
		// them can run Tables (R-182).
		largeRows := largeTableRowsOrDefault(cfg.LargeTableRows)
		for _, r := range s.Readers() {
			r.sortMemory, r.sortFile = cfg.SortMemory, cfg.SortFile
			r.largeTableRows = largeRows
		}
	}
	if err != nil {
		_ = sdb.Close()
		// The driver reports a read timeout only as "invalid connection", and
		// cannot tell it from a connection the server dropped, hence "may".
		if cfg.ReadTimeout > 0 && errors.Is(err, mysql.ErrInvalidConn) {
			err = fmt.Errorf("%w (the server may not have answered within the read timeout of %v)", err, cfg.ReadTimeout)
		}
		return nil, err
	}
	return s, nil
}

// pingWithin opens the first connection (dial, greeting and login) and gives up
// after timeout, so a server that accepts the TCP connection but never answers
// cannot hang Open even when no ReadTimeout is set. Zero means no limit. The
// pool reuses the connection, so start does not connect again.
func pingWithin(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := db.PingContext(cctx)
	if err != nil && cctx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("connect: no connection within the connect timeout of %v: %w", timeout, err)
	}
	return err
}

// pinSession runs the statements that pin every session setting that changes
// SHOW CREATE output or value text, so the dump never depends on server or
// account defaults: ANSI_QUOTES would quote identifiers with '"',
// PAD_CHAR_TO_FULL_LENGTH would pad CHAR values, NO_BACKSLASH_ESCAPES would
// change how SHOW CREATE escapes strings (schemafile relies on backslash
// escapes). s.dialect must be set.
func (s *Snapshot) pinSession(ctx context.Context) error {
	for _, stmt := range s.dialect.session {
		if _, err := s.conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// detectDialect picks s.dialect from the server version. It comes before
// pinSession, because the dialect decides which session settings exist.
func (s *Snapshot) detectDialect(ctx context.Context) error {
	var version string
	if err := s.conn.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return err
	}
	var err error
	s.dialect, err = dialectFor(version)
	return err
}

// lockGlobal runs FLUSH TABLES WITH READ LOCK on conn. It waits for running
// statements, and everything that arrives behind it waits too, so with a
// timeout above zero it gives up after that long (lock_wait_timeout, in whole
// seconds, set for this one statement) and the request leaves the queue. A
// timeout error says how to get past it; why is the caller's text. It returns
// the time the lock was taken, and reports the wait (Info `duration`) to lg.
func lockGlobal(ctx context.Context, conn *sql.Conn, timeout time.Duration, lg *slog.Logger, why string) (time.Time, error) {
	var secs int64
	if timeout > 0 {
		secs = lockWaitSeconds(timeout)
		if _, err := conn.ExecContext(ctx, "SET SESSION lock_wait_timeout = "+strconv.FormatInt(secs, 10)); err != nil {
			return time.Time{}, fmt.Errorf("SET SESSION lock_wait_timeout (%s): %w", why, err)
		}
		// Back to the server's value afterwards: the connection may go on to read.
		// The session value is only ever set here, so DEFAULT is the previous one.
		defer func() { _, _ = conn.ExecContext(ctx, "SET SESSION lock_wait_timeout = DEFAULT") }()
	}
	const stmt = "FLUSH TABLES WITH READ LOCK"
	lg.Debug("executing", "statement", stmt, "lock_wait_timeout_seconds", secs)
	begin := time.Now()
	_, err := conn.ExecContext(ctx, stmt)
	if err == nil {
		lockedAt := time.Now()
		lg.Info("global read lock acquired", "duration", lockedAt.Sub(begin))
		return lockedAt, nil
	}
	var me *mysql.MySQLError
	if timeout > 0 && errors.As(err, &me) && me.Number == errLockWaitTimeout {
		return time.Time{}, fmt.Errorf("FLUSH TABLES WITH READ LOCK did not get the lock within %d s (a running statement "+
			"did not finish; wait for it to end, or raise --lock-timeout, 0 for the server's own limit) (%s): %w",
			secs, why, err)
	}
	return time.Time{}, fmt.Errorf("FLUSH TABLES WITH READ LOCK (%s): %w", why, err)
}

// startSnapshot runs START TRANSACTION WITH CONSISTENT SNAPSHOT on conn.
func startSnapshot(ctx context.Context, conn *sql.Conn, lg *slog.Logger) error {
	const stmt = "START TRANSACTION WITH CONSISTENT SNAPSHOT"
	lg.Debug("executing", "statement", stmt)
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("%s: %w", stmt, err)
	}
	return nil
}

// lockWaitSeconds converts d (above zero) to whole seconds, rounding up and
// capping at the largest lock_wait_timeout. It avoids d+1s, which overflows
// for durations near the Duration limit.
func lockWaitSeconds(d time.Duration) int64 {
	secs := int64(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	return min(secs, maxLockWaitSeconds)
}

// start makes the Snapshot; lg must not be nil.
func start(ctx context.Context, sdb *sql.DB, dbs []string, noLock bool, jobs int, lockTimeout time.Duration, lg *slog.Logger) (*Snapshot, error) {
	if jobs > 1 {
		return startParallel(ctx, sdb, dbs, jobs, lockTimeout, lg)
	}
	conn, err := sdb.Conn(ctx)
	if err != nil {
		return nil, err
	}
	lg.Info("connected", "jobs", 1)
	s := &Snapshot{db: sdb, conn: conn, noLock: noLock, log: lg}
	if err := s.detectDialect(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := s.pinSession(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Before the lock, so a bad name never makes other sessions wait.
	if err := checkDatabases(ctx, conn, dbs); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// MVCC does not cover non-transactional tables (MyISAM and so on). When
	// there are any, hold a global read lock across the start of the snapshot
	// and the whole read, so they are from the same point in time. LOCK TABLES
	// is no option: it commits the snapshot transaction implicitly. Writes
	// elsewhere wait until Close.
	if !noLock {
		first, err := s.firstNonInnoDB(ctx, dbs)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if first != "" {
			s.lockedAt, err = lockGlobal(ctx, conn, lockTimeout, lg, fmt.Sprintf("needed because %s is not InnoDB; "+
				"it requires the RELOAD privilege, or pass --no-lock to read without consistency", first))
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			s.locked = true
		}
	}
	if err := startSnapshot(ctx, conn, lg); err != nil {
		_ = conn.Close()
		return nil, err
	}
	lg.Info("consistent snapshot started", "jobs", 1, "global_lock", s.locked)

	if err := s.Use(ctx, dbs[0]); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

// startParallel opens jobs connections on one point in time (design doc,
// section 6.1): a connection of its own takes FLUSH TABLES WITH READ LOCK,
// every reader starts its consistent snapshot while the lock is held, and the
// lock is released at once when every table is InnoDB (the snapshots cover
// them) or kept until Close when one is not. The lock also blocks DDL, so the
// engine check made under it cannot race.
//
// On failure every connection is returned to the pool, so the caller's
// sdb.Close ends the sessions, the transactions and the lock.
func startParallel(ctx context.Context, sdb *sql.DB, dbs []string, jobs int, lockTimeout time.Duration, lg *slog.Logger) (_ *Snapshot, err error) {
	var conns []*sql.Conn
	defer func() {
		if err != nil {
			for _, c := range conns {
				_ = c.Close()
			}
		}
	}()
	open := func() (*sql.Conn, error) {
		c, err := sdb.Conn(ctx)
		if err == nil {
			conns = append(conns, c)
		}
		return c, err
	}

	lockConn, err := open()
	if err != nil {
		return nil, err
	}
	readers := make([]*Snapshot, jobs)
	for i := range readers {
		c, err := open()
		if err != nil {
			return nil, err
		}
		readers[i] = &Snapshot{db: sdb, conn: c, log: lg}
	}
	// One more connection than jobs: the one that holds the lock.
	lg.Info("connected", "jobs", jobs, "connections", jobs+1)
	first := readers[0]
	if err := first.detectDialect(ctx); err != nil {
		return nil, err
	}
	for _, r := range readers {
		r.dialect = first.dialect
		if err := r.pinSession(ctx); err != nil {
			return nil, err
		}
	}

	// Before the lock, so a bad name never makes other sessions wait.
	if err := checkDatabases(ctx, first.conn, dbs); err != nil {
		return nil, err
	}
	lockedAt, err := lockGlobal(ctx, lockConn, lockTimeout, lg, fmt.Sprintf("needed to start the snapshots of %d connections "+
		"at one point in time; it requires the RELOAD privilege, or run with --jobs 1", jobs))
	if err != nil {
		return nil, err
	}
	nonInnoDB, err := first.firstNonInnoDB(ctx, dbs)
	if err != nil {
		return nil, err
	}
	for _, r := range readers {
		if err := startSnapshot(ctx, r.conn, lg); err != nil {
			return nil, err
		}
	}
	// A non-InnoDB table is not covered by the snapshots, so the lock stays
	// until Close; the readers then know the tables are consistent.
	if nonInnoDB == "" {
		lg.Debug("executing", "statement", "UNLOCK TABLES")
		if _, err := lockConn.ExecContext(ctx, "UNLOCK TABLES"); err != nil {
			return nil, fmt.Errorf("UNLOCK TABLES: %w", err)
		}
		lg.Info("global read lock released", "duration", time.Since(lockedAt))
	} else {
		first.lockedAt = lockedAt
	}
	for _, r := range readers {
		r.locked = nonInnoDB != ""
	}
	lg.Info("consistent snapshot started", "jobs", jobs, "global_lock", nonInnoDB != "")

	first.others, first.lockConn = readers[1:], lockConn
	if err := first.Use(ctx, dbs[0]); err != nil {
		return nil, err
	}
	return first, nil
}

// Use makes db the database the other methods read, without leaving the
// snapshot transaction, so every database read through one Snapshot comes
// from the same point in time. On error the Snapshot still points at the
// previous database. The other readers are switched after the first one has
// accepted db (same account, same server); if one of them fails, the readers
// may point at different databases and the caller must abort (dump.Run does).
func (s *Snapshot) Use(ctx context.Context, db string) error {
	for _, r := range s.Readers() {
		if err := r.use(ctx, db); err != nil {
			return err
		}
	}
	return nil
}

// checkDatabase fails unless db is not a system database and exists on the
// server, with exactly this spelling (the lookup itself may be case-insensitive,
// depending on the server's collation).
func checkDatabase(ctx context.Context, conn *sql.Conn, db string) error {
	if err := checkNotSystem(db); err != nil {
		return err
	}
	var found string
	err := conn.QueryRowContext(ctx,
		"SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", db).Scan(&found)
	if err == nil && found != db {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("database %q not found", db)
	}
	return err
}

// checkDatabases runs checkDatabase for every one of dbs, so a bad name is
// reported before any lock is taken or any data is read.
func checkDatabases(ctx context.Context, conn *sql.Conn, dbs []string) error {
	for _, db := range dbs {
		if err := checkDatabase(ctx, conn, db); err != nil {
			return err
		}
	}
	return nil
}

// use is Use for this connection alone.
func (s *Snapshot) use(ctx context.Context, db string) error {
	if err := checkDatabase(ctx, s.conn, db); err != nil {
		return err
	}
	// With db as the default database, SHOW CREATE VIEW leaves names in that
	// database unqualified, so the dump can be restored under another name.
	if _, err := s.conn.ExecContext(ctx, "USE "+sqlvalue.QuoteIdent(db)); err != nil {
		return err
	}
	s.name = db
	s.tableList, s.tableListed = nil, false
	return nil
}

// firstNonInnoDB describes the first base table of dbs (by database, then
// table name) that is not InnoDB, or returns "" if there is none.
func (s *Snapshot) firstNonInnoDB(ctx context.Context, dbs []string) (string, error) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(dbs)), ",")
	args := make([]any, len(dbs))
	for i, db := range dbs {
		args[i] = db
	}
	var schema, name, engine string
	err := s.conn.QueryRowContext(ctx,
		"SELECT TABLE_SCHEMA, TABLE_NAME, ENGINE FROM information_schema.TABLES"+
			" WHERE TABLE_SCHEMA IN ("+marks+") AND TABLE_TYPE = 'BASE TABLE' AND UPPER(ENGINE) <> 'INNODB'"+
			" ORDER BY TABLE_SCHEMA, TABLE_NAME LIMIT 1", args...).Scan(&schema, &name, &engine)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("table %s.%s (%s)", sqlvalue.QuoteIdent(schema), sqlvalue.QuoteIdent(name), engine), nil
}

// closeTimeout bounds the ROLLBACK in Close. It is a variable for tests.
var closeTimeout = 5 * time.Second

// Close ends the snapshot transaction and closes the connection, which also
// releases the global read lock if one is held. The ROLLBACK is bounded by
// closeTimeout; closing the connection ends the transaction anyway, so a
// stalled server cannot make Close hang.
func (s *Snapshot) Close() error {
	// Not derived from a caller context: Close must still run after the
	// dump was canceled.
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	// A failed or timed-out ROLLBACK is reported like any other cleanup
	// error; the dump is not silently treated as clean (see R-022). The
	// timeout is shared by all readers.
	readers := s.Readers()
	var errs []error
	for _, r := range readers {
		_, err := r.conn.ExecContext(ctx, "ROLLBACK")
		errs = append(errs, err)
	}
	// Every connection must go back to the pool before db.Close, which closes
	// the idle ones only: that is what ends the sessions and the lock.
	for _, r := range readers {
		errs = append(errs, r.conn.Close())
	}
	if s.lockConn != nil {
		errs = append(errs, s.lockConn.Close())
	}
	// The lock ended with its connection, so this is how long it was held; logged
	// after the close on purpose, as that is when it is released (R-112).
	if !s.lockedAt.IsZero() {
		s.log.Info("global read lock released", "duration", time.Since(s.lockedAt))
		s.lockedAt = time.Time{}
	}
	return errors.Join(append(errs, s.db.Close())...)
}
