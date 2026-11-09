// Package naming defines the dump file name scheme.
package naming

import (
	"fmt"
	"strings"
)

const (
	schemaSuffix = ".00-schema.sql"
	dataInfix    = ".10-data."
	sqlExt       = ".sql"
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

// DataFile returns the data file name of a table.
func DataFile(db, table string) string {
	return EscapeName(db) + dataInfix + EscapeName(table) + sqlExt
}

// IsOwnedBy reports whether file is the schema file or a data file of db.
func IsOwnedBy(file, db string) bool {
	if file == SchemaFile(db) {
		return true
	}
	return strings.HasPrefix(file, EscapeName(db)+dataInfix) && strings.HasSuffix(file, sqlExt)
}
