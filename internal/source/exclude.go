package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"mysqldance/internal/sqlvalue"
)

// SplitTableSpec splits a table given as DB.TABLE at the first dot. The name of
// a database that contains a dot cannot be written this way. Both parts must be
// non-empty.
func SplitTableSpec(spec string) (db, table string, err error) {
	db, table, ok := strings.Cut(spec, ".")
	if !ok || db == "" || table == "" {
		return "", "", fmt.Errorf("%q is not of the form DB.TABLE", spec)
	}
	return db, table, nil
}

// excludeSet is the tables left out of the dump, by database and then by table,
// spelled as the server's catalog spells them. Names are compared exactly.
type excludeSet map[string]map[string]bool

// has reports whether table of db is left out.
func (e excludeSet) has(db, table string) bool {
	return e[db][table]
}

// checkExcludeSpecs checks the form of every spec and that its database is one
// of dbs, without connecting. It returns the specs split into (db, table).
func checkExcludeSpecs(specs, dbs []string) ([][2]string, error) {
	var out [][2]string
	for _, spec := range specs {
		db, table, err := SplitTableSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("exclude table: %w", err)
		}
		if !slices.Contains(dbs, db) {
			return nil, fmt.Errorf("exclude table %q: database %q is not one of the databases to dump", spec, db)
		}
		out = append(out, [2]string{db, table})
	}
	return out, nil
}

// resolveExcludes checks on the server that every spec names a base table of
// a database being dumped, and returns the set with each name spelled as the
// catalog has it, so the later comparison follows the server's rules for the
// case of names (the same comparison as the catalog queries). A table that does
// not exist and a view are errors: a mistyped name must not pass silently.
func resolveExcludes(ctx context.Context, sdb *sql.DB, specs [][2]string) (excludeSet, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	conn, err := sdb.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	set := excludeSet{}
	checked := map[string]bool{}
	for _, sp := range specs {
		db, table := sp[0], sp[1]
		spec := db + "." + table
		if !checked[db] {
			if err := checkDatabase(ctx, conn, db); err != nil {
				return nil, err
			}
			checked[db] = true
		}
		var name, typ string
		err := conn.QueryRowContext(ctx,
			"SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?",
			db, table).Scan(&name, &typ)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("exclude table %q: no such table in database %q", spec, db)
		case err != nil:
			return nil, err
		case typ != "BASE TABLE":
			return nil, fmt.Errorf("exclude table %q: it is a %s, not a base table", spec, strings.ToLower(typ))
		}
		if set[db] == nil {
			set[db] = map[string]bool{}
		}
		set[db][name] = true
	}
	return set, nil
}

// excludes reports whether the table of the current database is left out.
func (s *Snapshot) excludes(table string) bool {
	return s.excluded.has(s.name, table)
}

// ExcludedRefs returns a warning for each view of the current database that
// reads a table left out of the dump, and for each foreign key of a table that
// is dumped that references one. The output keeps both as they are, so a restore
// may fail unless the table exists. A view is found by the server's own list of
// what it reads (MySQL 8.0.13+) or else by a text search of its definition, and
// a view of another database that reads a left-out table is not found.
func (s *Snapshot) ExcludedRefs(ctx context.Context) ([]string, error) {
	if len(s.excluded) == 0 {
		return nil, nil
	}
	list, err := s.listTables(ctx)
	if err != nil {
		return nil, err
	}
	var warnings []string
	var views []string
	for _, r := range list {
		if r.typ == "VIEW" {
			views = append(views, r.name)
		}
	}
	if own := s.excluded[s.name]; len(views) > 0 && len(own) > 0 {
		// The text search looks for the names it is given, so the left-out tables
		// are given with the views; the other lookup ignores the list.
		names := slices.Concat(views, slices.Sorted(maps.Keys(own)))
		found, _, err := s.dialect.viewDeps(ctx, s.conn, s.name, names)
		if err != nil {
			return nil, err
		}
		isView := map[string]bool{}
		for _, v := range views {
			isView[v] = true
		}
		for _, d := range found {
			if isView[d.view] && own[d.ref] {
				warnings = append(warnings, fmt.Sprintf(
					"view %s reads table %s, which is excluded; the output still refers to it, so restoring it may fail unless the table exists",
					sqlvalue.QuoteIdent(d.view), sqlvalue.QuoteIdent(d.ref)))
			}
		}
	}
	fks, err := s.excludedForeignKeys(ctx)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, fks...)
	slices.Sort(warnings)
	return slices.Compact(warnings), nil
}

// excludedForeignKeys warns about each foreign key, in a table of the current
// database that is dumped, that references a table left out of the dump.
func (s *Snapshot) excludedForeignKeys(ctx context.Context) ([]string, error) {
	rows, err := s.conn.QueryContext(ctx, s.dialect.defForeignKeys, s.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var table, name, col, refSchema, refTable, refCol, onUpdate, onDelete string
		if err := rows.Scan(&table, &name, &col, &refSchema, &refTable, &refCol, &onUpdate, &onDelete); err != nil {
			return nil, err
		}
		if s.excludes(table) || !s.excludedAnyCase(refSchema, refTable) {
			continue
		}
		out = append(out, fmt.Sprintf(
			"foreign key %s of table %s references table %s.%s, which is excluded; the output still refers to it, so restoring it may fail unless the table exists",
			sqlvalue.QuoteIdent(name), sqlvalue.QuoteIdent(table), sqlvalue.QuoteIdent(refSchema), sqlvalue.QuoteIdent(refTable)))
	}
	return out, rows.Err()
}

// excludedAnyCase is excludes for a table of any database, with the database
// compared without regard to case: the catalog may spell the schema of a
// reference differently (lower_case_table_names=2).
func (s *Snapshot) excludedAnyCase(db, table string) bool {
	for d, tables := range s.excluded {
		if !strings.EqualFold(d, db) {
			continue
		}
		for t := range tables {
			// Only a warning hangs on this, so a false match on a server that
			// tells two tables apart by case costs nothing.
			if strings.EqualFold(t, table) {
				return true
			}
		}
	}
	return false
}
