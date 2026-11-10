package writer

import (
	"io"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// MySQL writes MySQL 8.0-style SQL files.
type MySQL struct {
	data datafile.Options
}

// NewMySQL returns a MySQL writer. opt controls the INSERT statements and the
// splitting of large tables.
func NewMySQL(opt datafile.Options) *MySQL { return &MySQL{data: opt} }

// NeedsDefs implements Writer.
func (m *MySQL) NeedsDefs() bool { return false }

// WriteSchema implements Writer.
func (m *MySQL) WriteSchema(w io.Writer, s Schema) error {
	tables := make([]schemafile.Table, len(s.Tables))
	for i, t := range s.Tables {
		tables[i] = schemafile.Table{Name: t.Name, Create: schemafile.StripAutoIncrement(t.Create)}
	}
	byName := make(map[string]schemafile.View, len(s.Views))
	names := make([]string, len(s.Views))
	for i, v := range s.Views {
		byName[v.Name] = v
		names[i] = v.Name
	}
	ordered := make([]schemafile.View, 0, len(s.Views))
	for _, n := range schemafile.OrderViews(names, s.ViewDeps) {
		v := byName[n]
		ordered = append(ordered, schemafile.View{Name: v.Name, Create: schemafile.StripDefiner(v.Create)})
	}
	return schemafile.Write(w, s.DB, s.CreateDatabase, tables, ordered)
}

// DataParts implements Writer. The values are rendered as MySQL literals
// first; the split and the INSERT boundaries are decided on the rendered text.
// All rows of the table are held in memory (typed and rendered) until the parts
// are written; streaming is B-044 (review ledger R-030, R-044).
func (m *MySQL) DataParts(db string, t source.TableInfo, rows [][]sqlvalue.Value) []Part {
	return splitParts(t, rows, m.data.RowsPerFile, sqlvalue.Literals,
		func(w io.Writer, cols []string, group [][]string) error {
			return datafile.Write(w, db, t.Name, cols, t.ListColumns, group, m.data)
		})
}
