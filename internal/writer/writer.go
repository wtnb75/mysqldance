// Package writer defines the output writer interface and its implementations.
// A writer turns the structured schema and typed record values read by
// package source into the text of the dump files for one target DBMS.
package writer

import (
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

// Part writes one output file of a table's data.
type Part func(w io.Writer) error

// Writer renders a dump for one target DBMS. File names, staging and removal
// of stale files are not its concern; package dump owns them.
type Writer interface {
	// NeedsDefs reports whether WriteSchema builds the table definitions from
	// Schema.Defs, which costs extra catalog queries. A writer that passes the
	// MySQL definitions through returns false.
	NeedsDefs() bool
	// WriteSchema writes the schema file of one database.
	WriteSchema(w io.Writer, s Schema) error
	// DataParts splits the rows of a table, already in output order, into the
	// data files it is written to, and returns one Part per file. The result
	// is never empty: a table without rows still has one file. The number of
	// parts is known before any of them is written, so that dump can name and
	// check the files first. Each writer owns its split policy.
	DataParts(db string, t source.TableInfo, rows [][]sqlvalue.Value) []Part
}

// splitParts renders the rows of t with literal, divides them into the files
// they are written to (see datafile.Split) and returns one Part per file; write
// writes one file from the column names and the rendered rows of its group.
func splitParts(t source.TableInfo, rows [][]sqlvalue.Value, rowsPerFile uint32,
	literal func([]sqlvalue.Value) []string, write func(w io.Writer, cols []string, group [][]string) error) []Part {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = c.Name
	}
	literals := make([][]string, len(rows))
	for i, row := range rows {
		literals[i] = literal(row)
	}
	groups := datafile.Split(literals, keyColumns(t), rowsPerFile)
	parts := make([]Part, len(groups))
	for i, group := range groups {
		parts[i] = func(w io.Writer) error { return write(w, cols, group) }
	}
	return parts
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
