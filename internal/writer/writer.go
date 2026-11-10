// Package writer defines the output writer interface and its implementations.
// A writer turns the structured schema and typed record values read by
// package source into the text of the dump files for one target DBMS.
package writer

import (
	"fmt"
	"io"
	"slices"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// Schema is everything the schema file of one database is made of, as read
// from the source: nothing in it is yet adapted to a target DBMS. The table
// and view definitions (Create) are still MySQL SHOW CREATE text; a writer for
// another DBMS needs them as structure, which is for its own backlog item.
type Schema struct {
	DB             string
	CreateDatabase string
	Tables         []source.TableInfo
	Views          []schemafile.View
	// ViewDeps maps a view to the views it depends on.
	ViewDeps map[string][]string
	// Defs is the structure of each table by name. It is set only for a writer
	// whose NeedsDefs is true.
	Defs map[string]source.TableDef
	// Warn receives a message for each thing the writer could not carry over
	// to its target. nil discards them.
	Warn func(msg string)
}

// OpenFile opens the next data file of a table. The writer closes every file it
// opens, and reports the error of the close.
type OpenFile func() (io.WriteCloser, error)

// RowWriter receives the rows of one table, one at a time and already in
// output order, and writes them to its data files as they arrive.
type RowWriter interface {
	// Write renders and writes one row.
	Write(row []sqlvalue.Value) error
	// Close ends the last file. It must be called once, also after a failed
	// Write, so that the open file is closed. A table without rows still gets
	// one file.
	Close() error
}

// Writer renders a dump for one target DBMS. File names, staging and removal
// of stale files are not its concern; package dump owns them.
type Writer interface {
	// NeedsDefs reports whether WriteSchema builds the table definitions from
	// Schema.Defs, which costs extra catalog queries. A writer that passes the
	// MySQL definitions through returns false.
	NeedsDefs() bool
	// WriteSchema writes the schema file of one database.
	WriteSchema(w io.Writer, s Schema) error
	// NewRowWriter starts the data files of a table. The rows are not held: a
	// file is opened (through open) when its first row arrives, and the number
	// of files is known only after Close. Each writer owns its split policy.
	// The error is a table-level problem found before any row (a table the
	// target cannot hold); a bad row is reported by Write.
	NewRowWriter(db string, t source.TableInfo, open OpenFile) (RowWriter, error)
}

// emit passes each message to warn, if there is one.
func emit(warn func(string), msgs []string) {
	if warn == nil {
		return
	}
	for _, m := range msgs {
		warn(m)
	}
}

// outputColumns returns the columns of def that are written (all but the
// generated ones) and checks that they are the columns the data files carry.
func outputColumns(t source.TableInfo, def source.TableDef, warn func(format string, a ...any)) ([]source.ColumnDef, error) {
	var cols []source.ColumnDef
	for _, c := range def.Columns {
		if c.Generated {
			if warn != nil {
				warn("generated column %s is not output", sqlvalue.QuoteIdent(c.Name))
			}
			continue
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no column can be output")
	}
	if len(cols) != len(t.Columns) {
		return nil, fmt.Errorf("the definition has %d columns but the data has %d", len(cols), len(t.Columns))
	}
	for i, c := range cols {
		if c.Name != t.Columns[i].Name {
			return nil, fmt.Errorf("column %d is %s in the definition but %s in the data",
				i+1, sqlvalue.QuoteIdent(c.Name), sqlvalue.QuoteIdent(t.Columns[i].Name))
		}
	}
	return cols, nil
}

// ConstraintWriter is implemented by a writer whose target needs some
// definitions after all rows are loaded (foreign keys, which the files of
// different tables could otherwise violate in any order). dump writes the
// result to the database's constraints file (naming.ConstraintsFile), which
// sorts after every data file. A writer without it produces no such file.
type ConstraintWriter interface {
	// WriteConstraints writes the constraints file of one database.
	WriteConstraints(w io.Writer, s Schema) error
}

// streamWriter is the RowWriter of the writers whose data files are INSERT
// statements: it renders each row with literal and passes it to a
// datafile.Stream, which cuts statements and files by row content.
type streamWriter struct {
	stream  *datafile.Stream
	literal func([]sqlvalue.Value) ([]string, error)
}

// newStreamWriter returns a RowWriter for t. header and footer surround every
// file and quote renders identifiers (see datafile.WriteStatements).
func newStreamWriter(t source.TableInfo, open OpenFile, header, footer string, quote func(string) string,
	opt datafile.Options, literal func([]sqlvalue.Value) ([]string, error)) RowWriter {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = c.Name
	}
	return &streamWriter{
		stream:  datafile.NewStream(open, header, footer, quote, t.Name, cols, t.ListColumns, keyColumns(t), opt),
		literal: literal,
	}
}

func (w *streamWriter) Write(row []sqlvalue.Value) error {
	lit, err := w.literal(row)
	if err != nil {
		return err
	}
	return w.stream.Add(lit)
}

func (w *streamWriter) Close() error { return w.stream.Close() }

// infallible adapts a literal function that cannot fail.
func infallible(f func([]sqlvalue.Value) []string) func([]sqlvalue.Value) ([]string, error) {
	return func(row []sqlvalue.Value) ([]string, error) { return f(row), nil }
}

// keyColumns returns the indexes in a row of the columns the rows of t are
// ordered by, or nil when t has no such key or a key column is not part of the
// row (a generated column), in which case the whole row is used.
func keyColumns(t source.TableInfo) []int {
	idx := make([]int, 0, len(t.OrderBy))
	for _, k := range t.OrderBy {
		at := slices.IndexFunc(t.Columns, func(c sqlvalue.Column) bool { return c.Name == k })
		if at < 0 {
			return nil
		}
		idx = append(idx, at)
	}
	return idx
}
