// Package naming defines the dump file name scheme.
package naming

import (
	"fmt"
	"strings"
)

const (
	schemaSuffix      = ".00-schema.sql"
	dataInfix         = ".10-data."
	routinesSuffix    = ".15-routines.sql"
	viewsSuffix       = ".17-views.sql"
	triggersSuffix    = ".20-triggers.sql"
	eventsSuffix      = ".25-events.sql"
	constraintsSuffix = ".90-constraints.sql"
	sqlExt            = ".sql"
)

// EscapeName keeps [A-Za-z0-9_-] as is and percent-encodes every other
// UTF-8 byte (including '.' and '%') as %XX with upper-case hex.
func EscapeName(s string) string { return escapeName(s, true) }

// escapeName is EscapeName; keepUpper false escapes the upper-case letters too.
func escapeName(s string, keepUpper bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z' && keepUpper, c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// EscapeNameCaseSafe is EscapeName that also percent-encodes the ASCII
// upper-case letters (A -> %41), so two names that differ only in case give file
// names that differ in more than case: what stays literal is lower-case, and the
// letters of an escape are always upper-case hex.
func EscapeNameCaseSafe(s string) string { return escapeName(s, false) }

// The standalone functions below (SchemaFile ... DataFileParts) name files of
// one database and table without the others, so they cannot know whether the
// database part is shortened because of another table; a run uses a Plan built
// from all the tables (R-129). They use the plain escape only, never the
// case-safe one (NewPlanCaseSafe).

// SchemaFile returns the schema file name of a database.
func SchemaFile(db string) string {
	return NewPlan(db, nil).SchemaFile()
}

// RoutinesFile returns the name of the file that holds the stored procedures and
// functions of a database. "15" sorts after every data file (they continue with
// ".10-data.") and before the triggers file, because a trigger may call a
// routine.
func RoutinesFile(db string) string {
	return NewPlan(db, nil).RoutinesFile()
}

// ViewsFile returns the name of the file that holds the views of a database.
// "17" sorts after every data file (they continue with ".10-data.") and the
// routines file, because CREATE VIEW fails when the view calls a stored function
// that does not exist yet, and before the triggers and events files.
func ViewsFile(db string) string {
	return NewPlan(db, nil).ViewsFile()
}

// TriggersFile returns the name of the file that holds the triggers of a
// database. "20" sorts after every data file (they continue with ".10-data.")
// so loading the rows fires no trigger, and before the constraints file.
func TriggersFile(db string) string {
	return NewPlan(db, nil).TriggersFile()
}

// EventsFile returns the name of the file that holds the events of a database.
// "25" sorts after every data file (they continue with ".10-data."), the
// routines and the triggers (an event body may call or fire them), and before
// the constraints file.
func EventsFile(db string) string {
	return NewPlan(db, nil).EventsFile()
}

// ConstraintsFile returns the name of the file a writer may add after all data
// files of a database (foreign keys, applied once the rows are in). "90" sorts
// after "10", and no escaped table name can make a data file sort past it:
// data file names continue with ".10-data.".
func ConstraintsFile(db string) string {
	return NewPlan(db, nil).ConstraintsFile()
}

// DataFile returns the data file name of a table.
func DataFile(db, table string) string {
	return NewPlan(db, []string{table}).DataFile(table)
}

// minPartDigits is the least width of the part number in a split data file's
// name.
const minPartDigits = 4

// DataFileParts returns the data file names of a table written as n files, in
// lexicographic (= row) order. A single file keeps the name of DataFile; with
// more, each name gets a part number after the table name
// (`db.10-data.t.0001.sql`). The number is zero-padded to at least four digits,
// and to the digit count of n when that is larger, so every name has the same
// width and the lexicographic order stays the numeric order. The escaped table
// name never contains '.', so a part name cannot equal another table's file.
func DataFileParts(db, table string, n int) []string {
	return NewPlan(db, []string{table}).DataFileParts(table, n)
}

// dataFileParts builds the names from the database and table parts.
func dataFileParts(dbComp, tableComp string, n int) []string {
	if n <= 1 {
		return []string{dbComp + dataInfix + tableComp + sqlExt}
	}
	width := max(minPartDigits, len(fmt.Sprint(n)))
	prefix := dbComp + dataInfix + tableComp + "."
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s%0*d%s", prefix, width, i+1, sqlExt)
	}
	return names
}

// IsOwnedBy reports whether file is the schema file, the routines file, the
// views file, the triggers file, the events file, the constraints file or a
// data file of db, by its unshortened name (see Plan.IsOwned for the names of a run).
func IsOwnedBy(file, db string) bool {
	return isOwned(file, EscapeName(db))
}
