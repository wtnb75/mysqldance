package mask

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
)

// QuasiColumn is a column marked quasi: true, as the catalog spells it. DataType
// is the information_schema DATA_TYPE, which scan-k uses to compare a string
// column byte by byte.
type QuasiColumn struct {
	Name     string
	DataType string
}

// QuasiTable is the quasi-identifier of a table: all the columns of it that are
// marked quasi, in the order the table has them. The names are the catalog's
// spelling.
type QuasiTable struct {
	DB, Table string
	Columns   []QuasiColumn
}

// QuasiTables returns the tables that have a column marked quasi: true, in the
// order of the databases' names, then of the tables'. Every rule of the file is
// checked against the catalog first, as a dump does (see Resolve); a rule that
// has a transform is checked too, but no row is read, so no salt is needed. It
// is an error when no column is marked: a measurement of nothing must not pass
// as a measurement that found no fault.
func (r *Rules) QuasiTables(cat Catalog, reg *Registry) ([]QuasiTable, error) {
	// Nothing is transformed here, so the key a transform derives from this empty
	// salt is never used; it only lets a rule that needs a salt be checked. The
	// checks are those of a dump on purpose (B-196, review ledger R-209).
	if _, err := r.Resolve(cat, reg, &Salt{}); err != nil {
		return nil, err
	}
	marked := map[string]bool{}
	for _, e := range r.Entries {
		if !e.Rule.Quasi {
			continue
		}
		dbName, tbl, err := cat.lookupTable(e.Key)
		if err != nil {
			// Resolve accepted the rules, so this is a bug; never drop a mark silently.
			return nil, fmt.Errorf("rule %s: %w", e.Spec, err)
		}
		marked[cat.normKey(dbName, tbl.Name, e.Key.Column)] = true
	}
	if len(marked) == 0 {
		return nil, errors.New("no column is marked quasi: add \"quasi: true\" to the rule of each column that, with the others, can single out a person")
	}
	dbs := make([]string, 0, len(cat.Databases))
	for name := range cat.Databases {
		dbs = append(dbs, name)
	}
	slices.Sort(dbs)
	var out []QuasiTable
	for _, db := range dbs {
		tables := slices.Clone(cat.Databases[db])
		slices.SortFunc(tables, func(a, b CatalogTable) int { return cmp.Compare(a.Name, b.Name) })
		for _, tbl := range tables {
			if tbl.View || tbl.Excluded {
				continue
			}
			qt := QuasiTable{DB: db, Table: tbl.Name}
			for _, col := range tbl.Columns {
				if marked[cat.normKey(db, tbl.Name, col.Name)] {
					qt.Columns = append(qt.Columns, QuasiColumn{Name: col.Name, DataType: col.DataType})
				}
			}
			if len(qt.Columns) > 0 {
				out = append(out, qt)
			}
		}
	}
	if len(out) == 0 {
		// Every mark was found by Resolve, so this cannot happen; an empty report
		// must never pass as a measurement that found no fault.
		return nil, errors.New("no table with a column marked quasi is measured")
	}
	return out, nil
}
