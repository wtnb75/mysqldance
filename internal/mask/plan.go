package mask

import (
	"fmt"
	"strings"
	"sync/atomic"

	"mysqldance/internal/sqlvalue"
)

// plannedRule is a rule that was checked and prepared by Resolve.
type plannedRule struct {
	spec  string
	col   string
	index int
	fn    Func
	// use is the transform of the rule, for the usage report.
	use string
	// hits counts the rows whose value was not NULL when the transform was
	// called. It is nil for a rule with paths: those count in leaves.
	hits *atomic.Int64
	// leaves are the paths of a rule with paths, in the order of the file, that
	// change a value (a keep path is not here: it cannot miss).
	leaves []*pathLeaf
}

// planTable is what a table of the plan keeps besides its rules: its name, and
// the count of the rows that passed through its transforms.
type planTable struct {
	db, table string
	rows      atomic.Int64
}

// Plan is what Resolve makes of the rules: for each table, the transforms to
// apply to its rows. It is read only once built (the counters of the usage
// excepted, which are atomic), so the tables of a dump can use it from several
// goroutines.
type Plan struct {
	tables map[string][]plannedRule
	// info is the table of each key of tables, and order the keys in the order
	// the first rule of each table was added, so the usage is reported the same
	// way every time.
	info  map[string]*planTable
	order []string
	// checked is, for each table, the columns a UniqueChecked transform was
	// planned for; Resolve turns it into the unique checks (see Uniques).
	checked map[string]*checkedTable
	uniques uniqueChecks
}

func planKey(db, table string) string { return db + "\x00" + table }

// add records the rule for the column. db and the names are the catalog's
// spelling, the one Table is asked with. use is the transform's name. leaves
// are the paths of a rule with paths (nil for the other rules).
func (p *Plan) add(db, table string, col Column, spec, use string, fn Func, leaves []*pathLeaf) {
	key := planKey(db, table)
	if p.info == nil {
		p.info = map[string]*planTable{}
	}
	if p.info[key] == nil {
		p.info[key] = &planTable{db: db, table: table}
		p.order = append(p.order, key)
	}
	r := plannedRule{spec: spec, col: col.Name, index: col.Index, fn: fn, use: use, leaves: leaves}
	if leaves == nil {
		r.hits = new(atomic.Int64)
	}
	p.tables[key] = append(p.tables[key], r)
}

// RuleUsage is how many rows a rule was used on, for the report at the end of a
// dump. It holds names and counts, never a value.
type RuleUsage struct {
	DB, Table, Column string
	// Use is the name of the transform. Path is the JSON path of a rule with
	// paths ("" for a rule on the whole column).
	Use, Path string
	// Hits is the rows the rule was applied to: the rows whose value was
	// not NULL (a path: the rows where it found a value that is not a JSON null).
	// Rows is the rows of the table that were read.
	Hits, Rows int64
}

// Unused reports whether the rule did nothing on a table that has rows, which
// is what a misspelled path (or a column that is NULL in every row) looks like. A table with no rows is not
// a sign of a mistake.
func (u RuleUsage) Unused() bool { return u.Hits == 0 && u.Rows > 0 }

// Usage returns, for each rule that calls a transform, how many rows it was used
// on so far. keep and a rule that only marks a column quasi are not here: they
// cannot miss. The order is that of the tables as the rules first named them,
// then of the rules, so the same run gives the same report. It is meant for
// after the tables were read.
func (p *Plan) Usage() []RuleUsage {
	if p == nil {
		return nil
	}
	var out []RuleUsage
	for _, key := range p.order {
		info := p.info[key]
		rows := info.rows.Load()
		for _, r := range p.tables[key] {
			u := RuleUsage{DB: info.db, Table: info.table, Column: r.col, Use: r.use, Rows: rows}
			if r.hits != nil {
				u.Hits = r.hits.Load()
				out = append(out, u)
				continue
			}
			for _, l := range r.leaves {
				lu := u
				lu.Use, lu.Path, lu.Hits = l.use, l.path, l.hits.Load()
				out = append(out, lu)
			}
		}
	}
	return out
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
	info := p.info[planKey(db, table)]
	if info == nil {
		return nil, fmt.Errorf("table %s.%s: the plan has rules for it but no counters", db, table)
	}
	apply := func(row []sqlvalue.Value, r plannedRule) (sqlvalue.Value, error) {
		// A NULL is not a hit on purpose (B-172: a column that is NULL in every row
		// is reported as unused), and no transform writes over a NULL.
		if r.hits != nil && !row[r.index].IsNull() {
			r.hits.Add(1)
		}
		v, err := r.fn(Input{Value: row[r.index], Row: row, Index: r.index})
		if err != nil {
			return sqlvalue.Value{}, fmt.Errorf("column %q (rule %s): %w", r.col, r.spec, err)
		}
		return v, nil
	}
	if len(rules) == 1 {
		r := rules[0]
		return func(row []sqlvalue.Value) error {
			info.rows.Add(1)
			v, err := apply(row, r)
			if err != nil {
				return err
			}
			row[r.index] = v
			return nil
		}, nil
	}
	return func(row []sqlvalue.Value) error {
		info.rows.Add(1)
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
