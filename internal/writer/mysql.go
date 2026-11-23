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
	data        datafile.Options
	keepDefiner bool
}

// NewMySQL returns a MySQL writer. opt controls the INSERT statements and the
// splitting of large tables.
func NewMySQL(opt datafile.Options) *MySQL { return &MySQL{data: opt} }

// WithKeepDefiner sets whether views, routines, events and triggers keep the
// DEFINER clause as SHOW CREATE returned it (--definer=keep, B-111). The default
// removes it. It returns m.
func (m *MySQL) WithKeepDefiner(keep bool) *MySQL {
	m.keepDefiner = keep
	return m
}

// definer applies the DEFINER setting to one CREATE statement.
func (m *MySQL) definer(create string) string {
	if m.keepDefiner {
		return create
	}
	return schemafile.StripDefiner(create)
}

// NeedsDefs implements Writer.
func (m *MySQL) NeedsDefs() bool { return false }

// WriteSchema implements Writer.
func (m *MySQL) WriteSchema(w io.Writer, s Schema) error {
	tables := make([]schemafile.Table, len(s.Tables))
	for i, t := range s.Tables {
		tables[i] = schemafile.Table{Name: t.Name, Create: schemafile.StripAutoIncrement(t.Create)}
	}
	return schemafile.Write(w, s.DB, s.CreateDatabase, tables)
}

// WriteViews implements ViewWriter. The views are in their own file, after the
// routines, because CREATE VIEW fails when the view calls a stored function that
// does not exist yet (B-116). As for the other objects, the DEFINER clause is
// removed unless --definer=keep.
func (m *MySQL) WriteViews(w io.Writer, s Schema) error {
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
			emit(s.Warn, []Warning{{Message: fmt.Sprintf("view %s reads database %s: it must be restored before this one (files restore in file name order), or the CREATE VIEW fails",
				sqlvalue.QuoteIdent(v.Name), sqlvalue.QuoteIdent(db))}})
		}
	}
	ordered := make([]schemafile.View, 0, len(s.Views))
	for _, n := range schemafile.OrderViews(names, s.ViewDeps) {
		v := byName[n]
		ordered = append(ordered, schemafile.View{Name: v.Name, Create: m.definer(v.Create)})
	}
	return schemafile.WriteViews(w, s.DB, ordered)
}

// WriteRoutines implements RoutineWriter. The DEFINER clause is removed, as for
// views and triggers, so the routines belong to the restoring user. A routine
// with SQL SECURITY DEFINER then runs with that user's privileges.
func (m *MySQL) WriteRoutines(w io.Writer, s Schema) error {
	routines := make([]schemafile.Routine, len(s.Routines))
	for i, r := range s.Routines {
		r.Create = m.definer(r.Create)
		routines[i] = r
	}
	return schemafile.WriteRoutines(w, s.DB, routines)
}

// WriteEvents implements EventWriter. The DEFINER clause is removed, as for
// views, triggers and routines, so the events belong to the restoring user.
func (m *MySQL) WriteEvents(w io.Writer, s Schema) error {
	events := make([]schemafile.Event, len(s.Events))
	for i, e := range s.Events {
		e.Create = m.definer(e.Create)
		events[i] = e
	}
	return schemafile.WriteEvents(w, s.DB, events)
}

// WriteTriggers implements TriggerWriter. The DEFINER clause is removed, as for
// views, so the triggers belong to the restoring user and the name of the
// dumping user stays out of the output. The database name is removed from the
// trigger and table names too (MariaDB and 5.7 return them as written), so the
// dump restores under another database name.
func (m *MySQL) WriteTriggers(w io.Writer, s Schema) error {
	triggers := make([]schemafile.Trigger, len(s.Triggers))
	for i, t := range s.Triggers {
		create, err := schemafile.StripTriggerDB(m.definer(t.Create), s.DB)
		if err != nil {
			return fmt.Errorf("trigger %s: %w", sqlvalue.QuoteIdent(t.Name), err)
		}
		t.Create = create
		triggers[i] = t
	}
	return schemafile.WriteTriggers(w, s.DB, triggers)
}

// NewRowWriter implements Writer. Each value is rendered as a MySQL literal
// when its row arrives; the split and the INSERT boundaries are decided on the
// rendered text of that row and the bytes since the previous cut (see
// datafile.Write), so no row is held.
func (m *MySQL) NewRowWriter(db string, t source.TableInfo, open OpenFile) (RowWriter, error) {
	return newStreamWriter(t, open, datafile.Header(db), "", sqlvalue.QuoteIdent, m.data,
		infallible(sqlvalue.Literals)), nil
}
