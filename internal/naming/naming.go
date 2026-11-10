// Package naming defines the dump file name scheme.
package naming

import (
	"fmt"
	"strings"
)

const (
	schemaSuffix      = ".00-schema.sql"
	dataInfix         = ".10-data."
	triggersSuffix    = ".20-triggers.sql"
	constraintsSuffix = ".90-constraints.sql"
	sqlExt            = ".sql"
)

// EscapeName keeps [A-Za-z0-9_-] as is and percent-encodes every other
// UTF-8 byte (including '.' and '%') as %XX with upper-case hex.
func EscapeName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// SchemaFile returns the schema file name of a database.
func SchemaFile(db string) string {
	return EscapeName(db) + schemaSuffix
}

// TriggersFile returns the name of the file that holds the triggers of a
// database. "20" sorts after every data file (they continue with ".10-data.")
// so loading the rows fires no trigger, and before the constraints file.
func TriggersFile(db string) string {
	return EscapeName(db) + triggersSuffix
}

// ConstraintsFile returns the name of the file a writer may add after all data
// files of a database (foreign keys, applied once the rows are in). "90" sorts
// after "10", and no escaped table name can make a data file sort past it:
// data file names continue with ".10-data.".
func ConstraintsFile(db string) string {
	return EscapeName(db) + constraintsSuffix
}

// DataFile returns the data file name of a table.
func DataFile(db, table string) string {
	return EscapeName(db) + dataInfix + EscapeName(table) + sqlExt
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
	if n <= 1 {
		return []string{DataFile(db, table)}
	}
	width := max(minPartDigits, len(fmt.Sprint(n)))
	prefix := EscapeName(db) + dataInfix + EscapeName(table) + "."
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s%0*d%s", prefix, width, i+1, sqlExt)
	}
	return names
}

// IsOwnedBy reports whether file is the schema file, the triggers file, the
// constraints file or a data file of db.
func IsOwnedBy(file, db string) bool {
	if file == SchemaFile(db) || file == TriggersFile(db) || file == ConstraintsFile(db) {
		return true
	}
	return strings.HasPrefix(file, EscapeName(db)+dataInfix) && strings.HasSuffix(file, sqlExt)
}
