package source

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"mysqldance/internal/schemafile"
	"mysqldance/internal/sqlvalue"
)

// DefaultLargeTableRows is the Config.LargeTableRows used when none is set: the
// TABLE_ROWS estimate above which a table without a usable key triggers a
// warning. Its rows are sorted through temporary files, which is slow (a table
// with a key is streamed in the order the server gives).
const DefaultLargeTableRows = 500000

// largeTableRowsOrDefault returns DefaultLargeTableRows for zero. A negative
// threshold is kept: every table without a key is above it.
func largeTableRowsOrDefault(n int64) int64 {
	if n == 0 {
		return DefaultLargeTableRows
	}
	return n
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

// listTables returns the tables and views of the current database sorted by
// name. The list is read once per database (all readers are at the same point
// in time, so a later read would say the same); callers must not modify it.
func (s *Snapshot) listTables(ctx context.Context) ([]tableRow, error) {
	if s.tableListed {
		return s.tableList, nil
	}
	list, err := s.readTables(ctx)
	if err != nil {
		return nil, err
	}
	s.tableList, s.tableListed = list, true
	return list, nil
}

func (s *Snapshot) readTables(ctx context.Context) ([]tableRow, error) {
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
	name, dataType, columnType string
	nullable                   bool
	generated                  bool
	invisible                  bool
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
		if err := rows.Scan(&table, &c.name, &c.dataType, &c.columnType, &nullable, &extra); err != nil {
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
// without a key exceeds Config.LargeTableRows. Warnings follow table name order.
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
		for _, c := range columns[r.name] {
			if c.generated || c.invisible {
				ti.ListColumns = true
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
		if ti.Columns, err = s.tableColumns(ctx, r.name, columns[r.name]); err != nil {
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
		// A table with a key is streamed, and needs neither a sort nor scratch files.
		if len(ti.OrderBy) == 0 && ti.ApproxRows > s.largeTableRows {
			warnings = append(warnings, fmt.Sprintf(
				"table %s has about %d rows, far beyond the scale this tool is designed for; "+
					"its rows are sorted through temporary files because it has no primary key or NOT NULL unique key, which is slow for a table this large",
				sqlvalue.QuoteIdent(ti.Name), ti.ApproxRows))
		}
		out = append(out, ti)
	}
	return out, warnings, nil
}

// catalogTypeNames maps information_schema DATA_TYPE to the name the MySQL
// driver gives the same column (database/sql's DatabaseTypeName), minus the
// UNSIGNED prefix of the integer types. Only types whose name was checked
// against the driver (TestTableInfoColumnTypes) are listed; the others are
// read from the server instead.
var catalogTypeNames = map[string]string{
	"tinyint": "TINYINT", "smallint": "SMALLINT", "mediumint": "MEDIUMINT", "int": "INT", "bigint": "BIGINT",
	"decimal": "DECIMAL", "float": "FLOAT", "double": "DOUBLE", "bit": "BIT", "year": "YEAR",
	"date": "DATE", "datetime": "DATETIME", "timestamp": "TIMESTAMP", "time": "TIME",
	"char": "CHAR", "varchar": "VARCHAR", "binary": "BINARY", "varbinary": "VARBINARY",
	// The server sends one protocol type (BLOB) for all four sizes of TEXT and
	// of BLOB, so the driver's names do not tell the sizes apart.
	"tinytext": "TEXT", "text": "TEXT", "mediumtext": "TEXT", "longtext": "TEXT",
	"tinyblob": "BLOB", "blob": "BLOB", "mediumblob": "BLOB", "longblob": "BLOB",
	"enum": "ENUM", "set": "SET", "json": "JSON",
	"geometry": "GEOMETRY", "point": "GEOMETRY", "linestring": "GEOMETRY", "polygon": "GEOMETRY",
	"multipoint": "GEOMETRY", "multilinestring": "GEOMETRY", "multipolygon": "GEOMETRY",
	"geometrycollection": "GEOMETRY", "geomcollection": "GEOMETRY",
}

// catalogTypeName returns the driver's type name for a column described by its
// information_schema DATA_TYPE and COLUMN_TYPE, and false when the type has no
// mapping.
func catalogTypeName(dataType, columnType string) (string, bool) {
	name, ok := catalogTypeNames[strings.ToLower(dataType)]
	if !ok {
		return "", false
	}
	switch name {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT":
		// ZEROFILL implies UNSIGNED, and COLUMN_TYPE shows both.
		if strings.Contains(strings.ToLower(columnType), "unsigned") {
			name = "UNSIGNED " + name
		}
	}
	return name, true
}

// tableColumns returns the selectable (non-generated) columns of table with
// their type names. The names come from the catalog rows already read; if one
// column has a type without a mapping, they all come from the server, as a
// query for no rows (columnTypes).
func (s *Snapshot) tableColumns(ctx context.Context, table string, cols []columnRow) ([]sqlvalue.Column, error) {
	out := make([]sqlvalue.Column, 0, len(cols))
	names := make([]string, 0, len(cols))
	mapped := true
	for _, c := range cols {
		if c.generated {
			continue
		}
		names = append(names, c.name)
		name, ok := catalogTypeName(c.dataType, c.columnType)
		mapped = mapped && ok
		out = append(out, sqlvalue.Column{Name: c.name, TypeName: name})
	}
	if mapped {
		return out, nil
	}
	return s.columnTypes(ctx, table, names)
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
// statements and the other databases each reads (View.Reads), and deps[v]: the
// other views of this database that v references, sorted. Views without such
// references have no entry.
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
	found, foreign, err := s.dialect.viewDeps(ctx, s.conn, s.name, names)
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
	reads := map[string][]string{}
	for _, d := range foreign {
		if isView[d.view] {
			reads[d.view] = append(reads[d.view], d.ref)
		}
	}
	for i := range views {
		rs := reads[views[i].Name]
		sort.Strings(rs)
		views[i].Reads = compact(rs)
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
