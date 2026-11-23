package source

import (
	"context"
	"strings"
)

// Object is a table or view of the current database, with what the checks that
// run before a dump need to know of it (the masking rules are checked against
// it). Def and OrderBy are set for a base table that is not left out of the dump.
type Object struct {
	Name     string
	View     bool
	Excluded bool
	Def      TableDef
	OrderBy  []string
}

// Objects returns every table and view of the current database, sorted by name,
// the tables left out of the dump (Config.ExcludeTables) among them. It reads
// the catalog only: no row, no SHOW CREATE.
func (s *Snapshot) Objects(ctx context.Context) (_ []Object, err error) {
	defer func() { err = s.hint(err) }()
	list, err := s.listTables(ctx)
	if err != nil {
		return nil, err
	}
	defs, err := s.Defs(ctx)
	if err != nil {
		return nil, err
	}
	columns, err := s.listColumns(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := s.listUniqueKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Object, 0, len(list))
	for _, r := range list {
		o := Object{Name: r.name, View: r.typ == "VIEW"}
		switch {
		case o.View:
		case s.excludes(r.name):
			o.Excluded = true
		default:
			o.Def = defs[r.name]
			// The same key Tables orders the rows by.
			o.OrderBy = chooseKey(keys[r.name], columns[r.name])
		}
		out = append(out, o)
	}
	return out, nil
}

// FoldsTableCase reports whether the server compares the names of databases and
// tables without regard to case (lower_case_table_names is not 0), which is
// also how --exclude-table names are compared.
func (s *Snapshot) FoldsTableCase(ctx context.Context) (_ bool, err error) {
	defer func() { err = s.hint(err) }()
	var n int
	if err := s.conn.QueryRowContext(ctx, "SELECT @@lower_case_table_names").Scan(&n); err != nil {
		return false, err
	}
	return n != 0, nil
}

// CatalogTypeName returns the name the driver gives a column of the type
// (database/sql's DatabaseTypeName; an unsigned integer keeps the UNSIGNED
// prefix), given its DATA_TYPE and COLUMN_TYPE. A type without a known mapping
// gets its upper-cased DATA_TYPE.
func CatalogTypeName(dataType, columnType string) string {
	if name, ok := catalogTypeName(dataType, columnType); ok {
		return name
	}
	return strings.ToUpper(dataType)
}
