// Package source reads a consistent snapshot of one MySQL database: its
// CREATE statements, table metadata and rows formatted as SQL literals.
package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"mysqldance/internal/schemafile"
	"mysqldance/internal/sqlvalue"
)

// largeTableRows is the TABLE_ROWS estimate above which a table without a
// usable key triggers a warning: it is read fully into memory and sorted there
// (a table with a key is streamed). A variable so tests can lower it.
var largeTableRows int64 = 500000

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
}

// MaxJobs is the largest Config.Jobs: each job is a server connection.
const MaxJobs = 64

// DefaultLockTimeout is the LockTimeout the command line uses unless told
// otherwise.
const DefaultLockTimeout = 30 * time.Second

// maxLockWaitSeconds is the largest value of the lock_wait_timeout variable.
const maxLockWaitSeconds = 31536000

// errLockWaitTimeout is MySQL error 1205 (ER_LOCK_WAIT_TIMEOUT).
const errLockWaitTimeout = 1205

// TableInfo describes a base table.
type TableInfo struct {
	Name, Create, Engine string
	// Columns lists the non-generated columns in ordinal order.
	Columns []sqlvalue.Column
	// ListColumns is true when the table has generated or INVISIBLE columns,
	// so INSERT statements must name their columns.
	ListColumns bool
	// OrderBy is the primary key, else the first NOT NULL unique key by
	// index name, else empty.
	OrderBy []string
	// OrderByTypes holds the information_schema DATA_TYPE of each OrderBy
	// column, in the same order (same length as OrderBy). Rows uses it to
	// pick the sort expression.
	OrderByTypes []string
	// ApproxRows is the TABLE_ROWS estimate.
	ApproxRows int64
	// Def is the structure of the table. Tables does not fill it; the caller
	// sets it from Defs for a writer that builds its definitions from structure,
	// so that the writer sees it on every call without remembering anything.
	Def *TableDef
}

// Snapshot is a read-only consistent snapshot, held on a pinned connection
// inside one transaction. It reads one database at a time (the current one);
// Use switches databases without leaving the snapshot.
//
// With Config.Jobs above 1 the same point in time is also held by more
// connections, each a Snapshot of its own (see Readers). A Snapshot is for one
// goroutine at a time; the readers may run in parallel with each other.
type Snapshot struct {
	db      *sql.DB
	conn    *sql.Conn
	name    string
	dialect dialect
	// locked is true while the global read lock is held; noLock is the NoLock
	// option. A non-InnoDB table read with neither is an error.
	locked, noLock bool
	// others are the other readers, and lockConn is the connection that holds
	// the global read lock while the snapshots start. Both are set on the first
	// reader only (Config.Jobs above 1).
	others   []*Snapshot
	lockConn *sql.Conn
}

// Readers returns the connections that can read rows in parallel: the
// Snapshot itself first, then Config.Jobs-1 others. Each one has the same
// current database as the Snapshot (Use switches them all) and sees the same
// point in time. The metadata methods (CreateDatabase, Tables, Views, Defs)
// are for the Snapshot itself; Rows may run on any reader.
func (s *Snapshot) Readers() []*Snapshot {
	return append([]*Snapshot{s}, s.others...)
}

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
	if cfg.Socket != "" {
		mc.Net, mc.Addr = "unix", cfg.Socket
	} else {
		port := cfg.Port
		if host == "" {
			host = "127.0.0.1"
		}
		if port == 0 {
			port = 3306
		}
		mc.Net, mc.Addr = "tcp", net.JoinHostPort(host, strconv.Itoa(port))
	}
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
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	sdb := sql.OpenDB(connector)
	s, err := start(ctx, sdb, dbs, cfg.NoLock, cfg.Jobs, cfg.LockTimeout)
	if err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return s, nil
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
// timeout error says how to get past it; why is the caller's text.
func lockGlobal(ctx context.Context, conn *sql.Conn, timeout time.Duration, why string) error {
	var secs int64
	if timeout > 0 {
		secs = lockWaitSeconds(timeout)
		if _, err := conn.ExecContext(ctx, "SET SESSION lock_wait_timeout = "+strconv.FormatInt(secs, 10)); err != nil {
			return fmt.Errorf("SET SESSION lock_wait_timeout (%s): %w", why, err)
		}
		// Back to the server's value afterwards: the connection may go on to read.
		// The session value is only ever set here, so DEFAULT is the previous one.
		defer func() { _, _ = conn.ExecContext(ctx, "SET SESSION lock_wait_timeout = DEFAULT") }()
	}
	_, err := conn.ExecContext(ctx, "FLUSH TABLES WITH READ LOCK")
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if timeout > 0 && errors.As(err, &me) && me.Number == errLockWaitTimeout {
		return fmt.Errorf("FLUSH TABLES WITH READ LOCK did not get the lock within %d s (a running statement "+
			"did not finish; wait for it to end, or raise --lock-timeout, 0 for the server's own limit) (%s): %w",
			secs, why, err)
	}
	return fmt.Errorf("FLUSH TABLES WITH READ LOCK (%s): %w", why, err)
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

func start(ctx context.Context, sdb *sql.DB, dbs []string, noLock bool, jobs int, lockTimeout time.Duration) (*Snapshot, error) {
	if jobs > 1 {
		return startParallel(ctx, sdb, dbs, jobs, lockTimeout)
	}
	conn, err := sdb.Conn(ctx)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{db: sdb, conn: conn, noLock: noLock}
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
			if err := lockGlobal(ctx, conn, lockTimeout, fmt.Sprintf("needed because %s is not InnoDB; "+
				"it requires the RELOAD privilege, or pass --no-lock to read without consistency", first)); err != nil {
				_ = conn.Close()
				return nil, err
			}
			s.locked = true
		}
	}
	if _, err := conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("START TRANSACTION WITH CONSISTENT SNAPSHOT: %w", err)
	}

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
func startParallel(ctx context.Context, sdb *sql.DB, dbs []string, jobs int, lockTimeout time.Duration) (_ *Snapshot, err error) {
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
		readers[i] = &Snapshot{db: sdb, conn: c}
	}
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
	if err := lockGlobal(ctx, lockConn, lockTimeout, fmt.Sprintf("needed to start the snapshots of %d connections "+
		"at one point in time; it requires the RELOAD privilege, or run with --jobs 1", jobs)); err != nil {
		return nil, err
	}
	nonInnoDB, err := first.firstNonInnoDB(ctx, dbs)
	if err != nil {
		return nil, err
	}
	for _, r := range readers {
		if _, err := r.conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT"); err != nil {
			return nil, fmt.Errorf("START TRANSACTION WITH CONSISTENT SNAPSHOT: %w", err)
		}
	}
	// A non-InnoDB table is not covered by the snapshots, so the lock stays
	// until Close; the readers then know the tables are consistent.
	if nonInnoDB == "" {
		if _, err := lockConn.ExecContext(ctx, "UNLOCK TABLES"); err != nil {
			return nil, fmt.Errorf("UNLOCK TABLES: %w", err)
		}
	}
	for _, r := range readers {
		r.locked = nonInnoDB != ""
	}

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
	return errors.Join(append(errs, s.db.Close())...)
}

// CreateDatabase returns the statement from SHOW CREATE DATABASE.
func (s *Snapshot) CreateDatabase(ctx context.Context) (string, error) {
	var name, create string
	err := s.conn.QueryRowContext(ctx, "SHOW CREATE DATABASE "+sqlvalue.QuoteIdent(s.name)).Scan(&name, &create)
	return create, err
}

type tableRow struct {
	name, typ, engine string
	rows              int64
}

func (s *Snapshot) listTables(ctx context.Context) ([]tableRow, error) {
	rows, err := s.conn.QueryContext(ctx, s.dialect.tables, s.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []tableRow
	for rows.Next() {
		var r tableRow
		var engine sql.NullString
		var n sql.NullInt64
		if err := rows.Scan(&r.name, &r.typ, &engine, &n); err != nil {
			return nil, err
		}
		r.engine, r.rows = engine.String, n.Int64
		if err := checkTableType(r.name, r.typ); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// checkTableType accepts the two kinds of object a dump carries, tables and
// views. Any other TABLE_TYPE (a MariaDB SEQUENCE or SYSTEM VERSIONED table,
// whose history rows a plain SELECT does not return) would otherwise be left
// out of the output without a word, so it is an error.
func checkTableType(name, typ string) error {
	if typ == "BASE TABLE" || typ == "VIEW" {
		return nil
	}
	return fmt.Errorf("%s has the table type %q, which cannot be dumped; refusing to leave it out silently",
		sqlvalue.QuoteIdent(name), typ)
}

type columnRow struct {
	name, dataType string
	nullable       bool
	generated      bool
	invisible      bool
}

func (s *Snapshot) listColumns(ctx context.Context) (map[string][]columnRow, error) {
	rows, err := s.conn.QueryContext(ctx, s.dialect.columns, s.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]columnRow{}
	for rows.Next() {
		var table, nullable, extra string
		var c columnRow
		if err := rows.Scan(&table, &c.name, &c.dataType, &nullable, &extra); err != nil {
			return nil, err
		}
		c.nullable = nullable == "YES"
		// Match the exact markers: expression defaults show up as
		// DEFAULT_GENERATED and are ordinary columns.
		c.generated = strings.Contains(extra, "VIRTUAL GENERATED") || strings.Contains(extra, "STORED GENERATED")
		c.invisible = strings.Contains(extra, "INVISIBLE")
		out[table] = append(out[table], c)
	}
	return out, rows.Err()
}

// listUniqueKeys returns, per table, unique index name -> key columns.
// Indexes with functional key parts are left out.
func (s *Snapshot) listUniqueKeys(ctx context.Context) (map[string]map[string][]string, error) {
	rows, err := s.conn.QueryContext(ctx, s.dialect.uniqueKeys, s.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]map[string][]string{}
	functional := map[[2]string]bool{}
	for rows.Next() {
		var table, index string
		var col sql.NullString
		if err := rows.Scan(&table, &index, &col); err != nil {
			return nil, err
		}
		if !col.Valid {
			functional[[2]string{table, index}] = true
			continue
		}
		if out[table] == nil {
			out[table] = map[string][]string{}
		}
		out[table][index] = append(out[table][index], col.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k := range functional {
		delete(out[k[0]], k[1])
	}
	return out, nil
}

// chooseKey returns the primary key, else the NOT NULL unique key whose
// index name sorts first (byte order), else nil.
func chooseKey(keys map[string][]string, cols []columnRow) []string {
	if pk, ok := keys["PRIMARY"]; ok {
		return pk
	}
	nullable := map[string]bool{}
	for _, c := range cols {
		nullable[c.name] = c.nullable
	}
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ok := true
		for _, c := range keys[name] {
			if nullable[c] {
				ok = false
				break
			}
		}
		if ok {
			return keys[name]
		}
	}
	return nil
}

// Tables returns the base tables sorted by name, and the warnings found while
// reading them: tables not using InnoDB, and tables whose row estimate
// without a key exceeds largeTableRows. Warnings follow table name order.
func (s *Snapshot) Tables(ctx context.Context) ([]TableInfo, []string, error) {
	list, err := s.listTables(ctx)
	if err != nil {
		return nil, nil, err
	}
	columns, err := s.listColumns(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys, err := s.listUniqueKeys(ctx)
	if err != nil {
		return nil, nil, err
	}

	var out []TableInfo
	var warnings []string
	for _, r := range list {
		if r.typ != "BASE TABLE" {
			continue
		}
		ti := TableInfo{Name: r.name, Engine: r.engine, ApproxRows: r.rows}
		var names []string
		for _, c := range columns[r.name] {
			if c.generated || c.invisible {
				ti.ListColumns = true
			}
			if !c.generated {
				names = append(names, c.name)
			}
		}
		ti.OrderBy = chooseKey(keys[r.name], columns[r.name])
		ti.OrderByTypes = keyTypes(ti.OrderBy, columns[r.name])
		if ti.Create, err = s.showCreate(ctx, "TABLE", r.name); err != nil {
			return nil, nil, fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(r.name), err)
		}
		if err := checkAllColumnsVisible(ti.Create, columns[r.name]); err != nil {
			return nil, nil, fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(r.name), err)
		}
		if ti.Columns, err = s.columnTypes(ctx, r.name, names); err != nil {
			return nil, nil, fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(r.name), err)
		}
		if !strings.EqualFold(ti.Engine, "InnoDB") && !s.locked {
			if !s.noLock {
				// Created after Open decided that no lock was needed.
				return nil, nil, fmt.Errorf("table %s uses the %s engine and appeared after the dump started "+
					"without a global read lock; run again, or pass --no-lock to read without consistency",
					sqlvalue.QuoteIdent(ti.Name), ti.Engine)
			}
			warnings = append(warnings, fmt.Sprintf(
				"table %s uses the %s engine; its data is not covered by the consistent snapshot",
				sqlvalue.QuoteIdent(ti.Name), ti.Engine))
		}
		// A table with a key is streamed and needs no memory for its rows.
		if len(ti.OrderBy) == 0 && ti.ApproxRows > largeTableRows {
			warnings = append(warnings, fmt.Sprintf(
				"table %s has about %d rows, far beyond the scale this tool is designed for; "+
					"all its rows are held in memory and sorted there because it has no primary key or NOT NULL unique key",
				sqlvalue.QuoteIdent(ti.Name), ti.ApproxRows))
		}
		out = append(out, ti)
	}
	return out, warnings, nil
}

// keyTypes returns the DATA_TYPE of each key column, in key order.
func keyTypes(key []string, cols []columnRow) []string {
	if len(key) == 0 {
		return nil
	}
	typeOf := make(map[string]string, len(cols))
	for _, c := range cols {
		typeOf[c.name] = c.dataType
	}
	out := make([]string, len(key))
	for i, k := range key {
		out[i] = typeOf[k]
	}
	return out
}

// checkAllColumnsVisible fails unless the catalog lists every column that
// SHOW CREATE TABLE defines. information_schema.COLUMNS only shows columns the
// account has a privilege on, while SHOW CREATE TABLE shows them all, so a
// mismatch means column-level privileges hide data that would otherwise be
// dropped silently (INVISIBLE columns even pass SELECT *).
func checkAllColumnsVisible(create string, catalog []columnRow) error {
	defined, err := schemafile.ColumnNames(create)
	if err != nil {
		return err
	}
	visible := make(map[string]bool, len(catalog))
	for _, c := range catalog {
		visible[c.name] = true
	}
	var missing []string
	for _, name := range defined {
		if !visible[name] {
			missing = append(missing, sqlvalue.QuoteIdent(name))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the account cannot read column(s) %s (column-level privileges); refusing to dump the table partially",
			strings.Join(missing, ", "))
	}
	if len(defined) != len(catalog) {
		return fmt.Errorf("SHOW CREATE TABLE defines %d columns but information_schema lists %d", len(defined), len(catalog))
	}
	return nil
}

// columnTypes reads the driver's type names for the given columns.
func (s *Snapshot) columnTypes(ctx context.Context, table string, names []string) ([]sqlvalue.Column, error) {
	rows, err := s.conn.QueryContext(ctx, "SELECT "+columnList(names)+" FROM "+s.qualified(table)+" LIMIT 0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := make([]sqlvalue.Column, len(types))
	for i, ct := range types {
		out[i] = sqlvalue.Column{Name: names[i], TypeName: ct.DatabaseTypeName()}
	}
	return out, rows.Err()
}

func (s *Snapshot) showCreate(ctx context.Context, kind, name string) (string, error) {
	rows, err := s.conn.QueryContext(ctx, "SHOW CREATE "+kind+" "+s.qualified(name))
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("SHOW CREATE %s %s returned no rows", kind, name)
	}
	// The statement is the second column; SHOW CREATE VIEW adds more after it.
	vals := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range vals {
		dest[i] = &vals[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return "", err
	}
	return string(vals[1]), rows.Err()
}

// Views returns the views sorted by name with their SHOW CREATE VIEW
// statements, and deps[v]: the other views of this database that v
// references, sorted. Views without such references have no entry.
func (s *Snapshot) Views(ctx context.Context) ([]schemafile.View, map[string][]string, error) {
	list, err := s.listTables(ctx)
	if err != nil {
		return nil, nil, err
	}
	var views []schemafile.View
	isView := map[string]bool{}
	for _, r := range list {
		if r.typ != "VIEW" {
			continue
		}
		create, err := s.showCreate(ctx, "VIEW", r.name)
		if err != nil {
			return nil, nil, err
		}
		views = append(views, schemafile.View{Name: r.name, Create: create})
		isView[r.name] = true
	}

	names := make([]string, len(views))
	for i, v := range views {
		names[i] = v.Name
	}
	found, err := s.dialect.viewDeps(ctx, s.conn, s.name, names)
	if err != nil {
		return nil, nil, err
	}
	deps := map[string][]string{}
	for _, d := range found {
		if isView[d.view] && isView[d.ref] && d.view != d.ref {
			deps[d.view] = append(deps[d.view], d.ref)
		}
	}
	for v, ds := range deps {
		sort.Strings(ds)
		deps[v] = compact(ds)
	}
	return views, deps, nil
}

// compact removes adjacent duplicates from a sorted slice.
func compact(ss []string) []string {
	out := ss[:0]
	for i, v := range ss {
		if i == 0 || v != ss[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// stringKeyTypes are the DATA_TYPE values of key columns compared as binary
// in ORDER BY, so collations cannot change the order. Values distinct under a
// unique key's collation always differ in bytes, so the order stays total.
// ENUM/SET sort by their member index, which is already collation-independent.
var stringKeyTypes = map[string]bool{
	"char": true, "varchar": true,
	"tinytext": true, "text": true, "mediumtext": true, "longtext": true,
}

// Rows returns the rows of t as typed values, one []sqlvalue.Value per row in
// t.Columns order, collected from EachRow. It holds every row in memory; dump
// uses EachRow instead.
func (s *Snapshot) Rows(ctx context.Context, t TableInfo) ([][]sqlvalue.Value, error) {
	var out [][]sqlvalue.Value
	err := s.EachRow(ctx, t, func(row []sqlvalue.Value) error {
		out = append(out, row)
		return nil
	})
	return out, err
}

// EachRow calls fn with each row of t in output order, one []sqlvalue.Value per
// row in t.Columns order. With a key the server orders the rows and they are
// passed on as they arrive, so memory does not grow with the table; otherwise
// all rows are read first and sorted in memory by their MySQL literals joined
// with commas, in byte order. fn owns the row it is given. If fn fails, the
// read stops and EachRow returns its error. A read that ends early (a lost
// connection, a canceled context) is an error too, never a short table.
// t must come from Tables for the database currently selected by Use.
func (s *Snapshot) EachRow(ctx context.Context, t TableInfo, fn func(row []sqlvalue.Value) error) error {
	orderBy, err := orderByClause(t.OrderBy, t.OrderByTypes)
	if err != nil {
		return fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
	}
	query := "SELECT " + selectList(t.Columns) + " FROM " + s.qualified(t.Name) + orderBy

	// No query arguments: the text protocol delivers values as the server
	// formats them, and sql.RawBytes keeps them unconverted.
	rows, err := s.conn.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	// Closing after an early return reads the rest of the result off the wire:
	// the abort lasts up to one table scan, but the connection (and its snapshot)
	// stays usable (review ledger R-084).
	defer func() { _ = rows.Close() }()
	raw := make([]sql.RawBytes, len(t.Columns))
	dest := make([]any, len(raw))
	isFloat := make([]bool, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
		isFloat[i] = sqlvalue.IsFloat(t.Columns[i].TypeName)
	}

	emit := fn
	var held [][]sqlvalue.Value
	if len(t.OrderBy) == 0 {
		// Rendering here and again in the writer is accepted: B-045 replaces
		// this in-memory sort (review ledger R-045).
		emit = func(row []sqlvalue.Value) error {
			held = append(held, row)
			return nil
		}
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		row := make([]sqlvalue.Value, len(raw))
		for i, b := range raw {
			if b != nil && isFloat[i] {
				if b, err = sqlvalue.ExactFloat(b); err != nil {
					return fmt.Errorf("table %s, column %s: %w", sqlvalue.QuoteIdent(t.Name),
						sqlvalue.QuoteIdent(t.Columns[i].Name), err)
				}
			}
			row[i] = sqlvalue.NewValue(t.Columns[i].TypeName, b, b == nil)
		}
		if err := emit(row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(t.OrderBy) != 0 {
		return nil
	}
	keys := make([]string, len(held))
	for i, row := range held {
		keys[i] = strings.Join(sqlvalue.Literals(row), ",")
	}
	idx := make([]int, len(held))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	for _, i := range idx {
		if err := fn(held[i]); err != nil {
			return err
		}
	}
	return nil
}

// orderByClause returns the ORDER BY clause (with a leading space) for the
// key columns whose DATA_TYPEs are types, or "" for no key. It fails when a
// key column has no type: guessing would sort a string key by collation and
// make the output order depend on it.
func orderByClause(key, types []string) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	if len(types) != len(key) {
		return "", fmt.Errorf("key has %d columns but %d types", len(key), len(types))
	}
	terms := make([]string, len(key))
	for i, c := range key {
		if types[i] == "" {
			return "", fmt.Errorf("key column %s has no data type", sqlvalue.QuoteIdent(c))
		}
		terms[i] = sqlvalue.QuoteIdent(c)
		if stringKeyTypes[types[i]] {
			terms[i] = "CAST(" + terms[i] + " AS BINARY)"
		}
	}
	return " ORDER BY " + strings.Join(terms, ", "), nil
}

func (s *Snapshot) qualified(name string) string {
	return sqlvalue.QuoteIdent(s.name) + "." + sqlvalue.QuoteIdent(name)
}

// selectList returns the select list that reads cols. The text protocol prints
// a FLOAT with 6 significant digits, which loses bits, so a FLOAT is read as
// `col * 1e0`: its exact value as a DOUBLE, which keeps -0 too (B-059). It does
// not use CAST(... AS DOUBLE), which MySQL 5.7 and 8.0 before 8.0.17 lack.
// EachRow turns that text back into the FLOAT's own notation.
func selectList(cols []sqlvalue.Column) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = sqlvalue.QuoteIdent(c.Name)
		if sqlvalue.IsFloat(c.TypeName) {
			q[i] += " * 1e0"
		}
	}
	return strings.Join(q, ", ")
}

func columnList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = sqlvalue.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}
