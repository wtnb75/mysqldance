// Package writer defines the output writer interface and its implementations.
// A writer turns the structured schema and typed record values read by
// package source into the text of the dump files for one target DBMS.
package writer

import (
	"io"

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
}

// Part writes one output file of a table's data.
type Part func(w io.Writer) error

// Writer renders a dump for one target DBMS. File names, staging and removal
// of stale files are not its concern; package dump owns them.
type Writer interface {
	// WriteSchema writes the schema file of one database.
	WriteSchema(w io.Writer, s Schema) error
	// DataParts splits the rows of a table, already in output order, into the
	// data files it is written to, and returns one Part per file. The result
	// is never empty: a table without rows still has one file. The number of
	// parts is known before any of them is written, so that dump can name and
	// check the files first. Each writer owns its split policy.
	DataParts(db string, t source.TableInfo, rows [][]sqlvalue.Value) []Part
}
