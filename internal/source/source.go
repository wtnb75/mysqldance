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

// largeTableRows is the TABLE_ROWS estimate above which a table triggers a
// warning: every table is read fully into memory (and sorted there when it
// has no usable key). A variable so tests can lower it.
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
}

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

// Snapshot is a read-only consistent snapshot, held on a single pinned
// connection inside one transaction. It reads one database at a time (the
// current one); Use switches databases without leaving the snapshot.
type Snapshot struct {
	db      *sql.DB
	conn    *sql.Conn
	name    string
	dialect dialect
	// locked is true while the global read lock is held; noLock is the NoLock
	// option. A non-InnoDB table read with neither is an error.
	locked, noLock bool
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
// (unless cfg.NoLock), so those tables are consistent with the rest.
func Open(ctx context.Context, cfg Config, dbs ...string) (*Snapshot, error) {
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
	// A bad ssl setting must fail here, as the first thing Run does after
	// creating the output directory, so it aborts before any output file is
	// deleted or written.
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
	s, err := start(ctx, sdb, dbs, cfg.NoLock)
	if err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return s, nil
}

func start(ctx context.Context, sdb *sql.DB, dbs []string, noLock bool) (*Snapshot, error) {
	conn, err := sdb.Conn(ctx)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{db: sdb, conn: conn, noLock: noLock}
	// The dialect decides which session settings exist, so it comes first.
	var version string
	if err := conn.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if s.dialect, err = dialectFor(version); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// Pin every session setting that changes SHOW CREATE output or value
	// text, so the dump never depends on server or account defaults:
	// ANSI_QUOTES would quote identifiers with '"', PAD_CHAR_TO_FULL_LENGTH
	// would pad CHAR values, NO_BACKSLASH_ESCAPES would change how SHOW
	// CREATE escapes strings (schemafile relies on backslash escapes).
	for _, stmt := range s.dialect.session {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%s: %w", stmt, err)
		}
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
			if _, err := conn.ExecContext(ctx, "FLUSH TABLES WITH READ LOCK"); err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("FLUSH TABLES WITH READ LOCK (needed because %s is not InnoDB; "+
					"it requires the RELOAD privilege, or pass --no-lock to read without consistency): %w", first, err)
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

// Use makes db the database the other methods read, without leaving the
// snapshot transaction, so every database read through one Snapshot comes
// from the same point in time. On error the Snapshot still points at the
// previous database.
func (s *Snapshot) Use(ctx context.Context, db string) error {
	if err := checkNotSystem(db); err != nil {
		return err
	}
	var found string
	err := s.conn.QueryRowContext(ctx,
		"SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", db).Scan(&found)
	if err == nil && found != db {
		err = sql.ErrNoRows
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("database %q not found", db)
		}
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
	// error; the dump is not silently treated as clean (see R-022).
	_, rbErr := s.conn.ExecContext(ctx, "ROLLBACK")
	return errors.Join(rbErr, s.conn.Close(), s.db.Close())
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
// exceeds largeTableRows. Warnings follow table name order.
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
		if ti.ApproxRows > largeTableRows {
			how := "all its rows are held in memory while its data file is written"
			if len(ti.OrderBy) == 0 {
				how += ", and sorted there because it has no primary key or NOT NULL unique key"
			}
			warnings = append(warnings, fmt.Sprintf(
				"table %s has about %d rows, far beyond the scale this tool is designed for; %s",
				sqlvalue.QuoteIdent(ti.Name), ti.ApproxRows, how))
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
// t.Columns order. With a key the server orders them; otherwise they are
// sorted in memory by their MySQL literals joined with commas, in byte order.
// t must come from Tables for the database currently selected by Use.
func (s *Snapshot) Rows(ctx context.Context, t TableInfo) ([][]sqlvalue.Value, error) {
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	orderBy, err := orderByClause(t.OrderBy, t.OrderByTypes)
	if err != nil {
		return nil, fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
	}
	query := "SELECT " + columnList(names) + " FROM " + s.qualified(t.Name) + orderBy

	// No query arguments: the text protocol delivers values as the server
	// formats them, and sql.RawBytes keeps them unconverted.
	rows, err := s.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	raw := make([]sql.RawBytes, len(t.Columns))
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	var out [][]sqlvalue.Value
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]sqlvalue.Value, len(raw))
		for i, b := range raw {
			row[i] = sqlvalue.NewValue(t.Columns[i].TypeName, b, b == nil)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(t.OrderBy) == 0 {
		// Rendering here and again in dump is accepted: B-045 replaces this
		// in-memory sort (review ledger R-045).
		keys := make([]string, len(out))
		for i, row := range out {
			keys[i] = strings.Join(sqlvalue.Literals(row), ",")
		}
		idx := make([]int, len(out))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
		sorted := make([][]sqlvalue.Value, len(out))
		for i, j := range idx {
			sorted[i] = out[j]
		}
		out = sorted
	}
	return out, nil
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

func columnList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = sqlvalue.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}
