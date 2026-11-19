package mask

import (
	"errors"
	"fmt"
	"strings"
)

// CatalogColumn is a column of a table as the catalog lists it. TypeName is the
// driver's type name (sqlvalue.Column TypeName). A Generated column is not
// output, so a rule on it could never apply.
type CatalogColumn struct {
	Name      string
	TypeName  string
	Generated bool
}

// CatalogTable is a table or view of a database being dumped. Excluded marks a
// table left out of the dump by --exclude-table.
type CatalogTable struct {
	Name     string
	View     bool
	Excluded bool
	Columns  []CatalogColumn
}

// Catalog is what the rules are checked against: the databases being dumped
// and their tables. A database that is not a key of Databases is not dumped
// (or does not exist). FoldTableCase is set when the server compares table
// names without regard to case (lower_case_table_names is not 0); column names
// are always compared without regard to case.
type Catalog struct {
	FoldTableCase bool
	Databases     map[string][]CatalogTable
}

func (c Catalog) sameTable(a, b string) bool {
	if c.FoldTableCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// database finds a database being dumped, by the catalog's spelling, which it
// also returns. Database names follow the same rule as table names.
func (c Catalog) database(name string) (string, []CatalogTable, bool) {
	if tables, ok := c.Databases[name]; ok {
		return name, tables, true
	}
	if c.FoldTableCase {
		for db, tables := range c.Databases {
			if strings.EqualFold(db, name) {
				return db, tables, true
			}
		}
	}
	return "", nil, false
}

// Validate checks every rule against the catalog and the registry, before any
// output is written. It reports every problem found (joined, in file order):
// a database that is not dumped, a table that does not exist, is a view or is
// excluded, a column that does not exist or is generated, two rules for one
// column, and a transform that does not apply to the type of its column.
func (r *Rules) Validate(cat Catalog, reg *Registry) error {
	var errs []error
	seen := map[string]string{} // normalized key -> spec of the first rule
	for _, e := range r.Entries {
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("rule %s (line %d): %s", e.Spec, e.Line, fmt.Sprintf(format, args...)))
		}
		dbName, tables, ok := cat.database(e.Key.DB)
		if !ok {
			bad("database %q is not one of the databases to dump", e.Key.DB)
			continue
		}
		var tbl *CatalogTable
		for i := range tables {
			if cat.sameTable(tables[i].Name, e.Key.Table) {
				tbl = &tables[i]
				break
			}
		}
		switch {
		case tbl == nil:
			bad("no such table %q in database %q", e.Key.Table, e.Key.DB)
			continue
		case tbl.View:
			bad("%q is a view, not a base table; its rows are not output", e.Key.Table)
			continue
		case tbl.Excluded:
			bad("table %q is excluded from the dump", e.Key.Table)
			continue
		}
		var col *CatalogColumn
		for i := range tbl.Columns {
			if strings.EqualFold(tbl.Columns[i].Name, e.Key.Column) {
				col = &tbl.Columns[i]
				break
			}
		}
		switch {
		case col == nil:
			bad("no such column %q in table %q", e.Key.Column, tbl.Name)
			continue
		case col.Generated:
			bad("column %q of table %q is generated, so it is not output", col.Name, tbl.Name)
			continue
		}
		// The catalog's spelling names the column, so two spellings of one
		// column meet here.
		tblKey := tbl.Name
		if cat.FoldTableCase {
			tblKey = strings.ToLower(tblKey)
		}
		norm := dbName + "\x00" + tblKey + "\x00" + strings.ToLower(col.Name)
		if first, dup := seen[norm]; dup {
			bad("column %q of table %q already has a rule (%s)", col.Name, tbl.Name, first)
			continue
		}
		seen[norm] = e.Spec
		if err := checkApplies(e, *col, reg); err != nil {
			bad("%v", err)
		}
	}
	return errors.Join(errs...)
}

// checkApplies checks the transform of a rule against the type of the column.
// A rule given per JSON path needs a JSON column; the type of what a path
// reaches is not known here, so the transforms below a path are checked for
// their names and arguments only (when the file is parsed).
func checkApplies(e Entry, col CatalogColumn, reg *Registry) error {
	if len(e.Rule.Paths) > 0 {
		if !strings.EqualFold(col.TypeName, "JSON") {
			return fmt.Errorf("paths can only be given for a JSON column, but %q is %s", col.Name, col.TypeName)
		}
		return nil
	}
	t, ok := reg.Lookup(e.Rule.Use)
	if !ok {
		return fmt.Errorf("unknown transform %q", e.Rule.Use)
	}
	if !t.appliesTo(col.TypeName) {
		return fmt.Errorf("transform %q does not apply to column %q of type %s (applies to: %s)",
			t.Name, col.Name, col.TypeName, strings.Join(t.Types, ", "))
	}
	return nil
}
