package mask

import (
	"fmt"
	"strings"

	"mysqldance/internal/sqlvalue"
)

// plannedRule is a rule that was checked and prepared by Resolve.
type plannedRule struct {
	spec  string
	col   string
	index int
	fn    Func
}

// Plan is what Resolve makes of the rules: for each table, the transforms to
// apply to its rows. It is read only once built, so the tables of a dump can
// use it from several goroutines.
type Plan struct {
	tables map[string][]plannedRule
	// checked is, for each table, the columns a UniqueChecked transform was
	// planned for; Resolve turns it into the unique checks (see Uniques).
	checked map[string]*checkedTable
	uniques uniqueChecks
}

func planKey(db, table string) string { return db + "\x00" + table }

// add records the rule for the column. db and the names are the catalog's
// spelling, the one Table is asked with.
func (p *Plan) add(db, table string, col Column, spec string, fn Func) {
	key := planKey(db, table)
	p.tables[key] = append(p.tables[key], plannedRule{spec: spec, col: col.Name, index: col.Index, fn: fn})
}

// Empty reports whether the plan has no rule for any table.
func (p *Plan) Empty() bool { return p == nil || len(p.tables) == 0 }

// Table returns the function that transforms a row of the table in place, or
// nil when no rule is for the table (so a table without a rule pays nothing per
// row). cols are the columns of the rows, as the dump reads them: the check
// that they are the columns the rules were prepared for makes a table that
// changed between the check and the read an error, never a rule applied to
// another column.
//
// Every transform sees the row as it was read, whatever the rules before it did
// to other columns, so the order of the rules in the file does not matter. An
// error names the column and the rule and never a value.
func (p *Plan) Table(db, table string, cols []sqlvalue.Column) (func(row []sqlvalue.Value) error, error) {
	if p == nil {
		return nil, nil
	}
	rules := p.tables[planKey(db, table)]
	if len(rules) == 0 {
		return nil, nil
	}
	for _, r := range rules {
		if r.index < 0 || r.index >= len(cols) || !strings.EqualFold(cols[r.index].Name, r.col) {
			return nil, fmt.Errorf("rule %s: column %q is not where it was when the rules were checked", r.spec, r.col)
		}
		if r.fn == nil {
			return nil, fmt.Errorf("rule %s: the transform has no implementation", r.spec)
		}
	}
	apply := func(row []sqlvalue.Value, r plannedRule) (sqlvalue.Value, error) {
		v, err := r.fn(Input{Value: row[r.index], Row: row, Index: r.index})
		if err != nil {
			return sqlvalue.Value{}, fmt.Errorf("column %q (rule %s): %w", r.col, r.spec, err)
		}
		return v, nil
	}
	if len(rules) == 1 {
		r := rules[0]
		return func(row []sqlvalue.Value) error {
			v, err := apply(row, r)
			if err != nil {
				return err
			}
			row[r.index] = v
			return nil
		}, nil
	}
	return func(row []sqlvalue.Value) error {
		out := make([]sqlvalue.Value, len(rules))
		for i, r := range rules {
			v, err := apply(row, r)
			if err != nil {
				return err
			}
			out[i] = v
		}
		for i, r := range rules {
			row[r.index] = out[i]
		}
		return nil
	}, nil
}
