package mask

import (
	"fmt"
	"slices"
	"strings"

	"mysqldance/internal/sqlvalue"
)

// UniqueCheck is a unique index of a table that has a transformed column: the
// dump compares the rows on it after the transform, because the transform can
// give two rows one value (a UniqueChecked transform is the one that may be used
// on such a column).
type UniqueCheck struct {
	// Index is the name of the index and Columns the names of its columns in key
	// order, for the message of an error.
	Index   string
	Columns []string
	// Positions are the positions of the columns in a row, and Fold says for each
	// column whether the collation compares without regard to the case of ASCII
	// letters (its name has _ci).
	Positions []int
	Fold      []bool
}

// checkedTable is a table with columns that took a UniqueChecked transform.
type checkedTable struct {
	tbl     CatalogTable
	columns []string
}

// noteChecked records that column of tbl took a UniqueChecked transform.
func (p *Plan) noteChecked(db string, tbl CatalogTable, column string) {
	key := planKey(db, tbl.Name)
	ct := p.checked[key]
	if ct == nil {
		ct = &checkedTable{tbl: tbl}
		p.checked[key] = ct
	}
	ct.columns = append(ct.columns, column)
}

// uniques holds the checks built by buildUniques, by table.
type uniqueChecks map[string][]UniqueCheck

// buildUniques turns the noted columns into the checks: one for each unique
// index (the primary key is never one: a transform that breaks keys is refused
// on it) that has a noted column. Indexes are in name order, so the checks do
// not depend on the order of the rules.
func (p *Plan) buildUniques() {
	p.uniques = uniqueChecks{}
	for key, ct := range p.checked {
		cols := tableColumns(&ct.tbl)
		indexes := slices.Clone(ct.tbl.Indexes)
		slices.SortFunc(indexes, func(a, b CatalogIndex) int { return strings.Compare(a.Name, b.Name) })
		var checks []UniqueCheck
		for _, ix := range indexes {
			if !ix.Unique || ix.Name == "PRIMARY" || !slices.ContainsFunc(ix.Columns, func(c string) bool { return containsFold(ct.columns, c) }) {
				continue
			}
			uc := UniqueCheck{Index: ix.Name}
			for _, name := range ix.Columns {
				c := columnOf(cols, name)
				uc.Columns = append(uc.Columns, c.Name)
				uc.Positions = append(uc.Positions, c.Index)
				uc.Fold = append(uc.Fold, foldsCase(ct.tbl, name))
			}
			checks = append(checks, uc)
		}
		if len(checks) > 0 {
			p.uniques[key] = checks
		}
	}
}

// foldsCase reports whether the collation of the column compares ASCII letters
// without regard to their case. Only that is folded: an accent, a kana or a
// full-width form that the collation treats as the same is not detected. A column
// without a collation (a number, a date, binary data) compares by bytes, which is
// right for it; a string column always has one (review ledger R-232).
func foldsCase(tbl CatalogTable, column string) bool {
	for _, c := range tbl.Columns {
		if strings.EqualFold(c.Name, column) {
			return strings.Contains(strings.ToLower(c.Collation), "_ci")
		}
	}
	return false
}

// Uniques returns the unique checks of the table, or nil when it has none. cols
// are the columns of the rows, as for Table: the positions in the checks must
// still be those of the columns they were made for.
func (p *Plan) Uniques(db, table string, cols []sqlvalue.Column) ([]UniqueCheck, error) {
	if p == nil {
		return nil, nil
	}
	checks := p.uniques[planKey(db, table)]
	for _, uc := range checks {
		for i, pos := range uc.Positions {
			if pos < 0 || pos >= len(cols) || !strings.EqualFold(cols[pos].Name, uc.Columns[i]) {
				return nil, fmt.Errorf("unique index %q: column %q is not where it was when the rules were checked", uc.Index, uc.Columns[i])
			}
		}
	}
	return checks, nil
}
