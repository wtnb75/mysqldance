package writer

import (
	"fmt"
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
		// Files restore in file name order, and no order is kept between the
		// files of two databases, so say what the view needs (B-023). Always
		// warn, whatever the file order: the restore target is unknown (R-151).
		// The SQLite and PostgreSQL writers output no views at all (R-152).
		for _, db := range v.Reads {
			emit(s.Warn, []string{fmt.Sprintf("view %s reads database %s: it must be restored before this one (files restore in file name order), or the CREATE VIEW fails",
				sqlvalue.QuoteIdent(v.Name), sqlvalue.QuoteIdent(db))})
		}
	}
	ordered := make([]schemafile.View, 0, len(s.Views))
	for _, n := range schemafile.OrderViews(names, s.ViewDeps) {
		v := byName[n]
		ordered = append(ordered, schemafile.View{Name: v.Name, Create: schemafile.StripDefiner(v.Create)})
	}
	return schemafile.Write(w, s.DB, s.CreateDatabase, tables, ordered)
}

// WriteTriggers implements TriggerWriter. The DEFINER clause is removed, as for
// views, so the triggers belong to the restoring user and the name of the
// dumping user stays out of the output.
func (m *MySQL) WriteTriggers(w io.Writer, s Schema) error {
	triggers := make([]schemafile.Trigger, len(s.Triggers))
	for i, t := range s.Triggers {
		t.Create = schemafile.StripDefiner(t.Create)
		triggers[i] = t
	}
	return schemafile.WriteTriggers(w, s.DB, triggers)
}

// NewRowWriter implements Writer. Each value is rendered as a MySQL literal
// when its row arrives; the split and the INSERT boundaries are decided on the
// rendered text of that row alone, so no row is held.
func (m *MySQL) NewRowWriter(db string, t source.TableInfo, open OpenFile) (RowWriter, error) {
	return newStreamWriter(t, open, datafile.Header(db), "", sqlvalue.QuoteIdent, m.data,
		infallible(sqlvalue.Literals)), nil
}
