// Package schemafile builds the schema dump file: CREATE statements for
// tables and views, with helpers to normalize SHOW CREATE output.
package schemafile

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"mysqldance/internal/datafile"
)

var (
	autoIncrementOption = regexp.MustCompile(` AUTO_INCREMENT=\d+`)
	ident               = "(?:`(?:[^`]|``)*`|[^\\s@`]+)"
	definerClause       = regexp.MustCompile("DEFINER=" + ident + "@" + ident + " ?")
)

// Table is a table name with its CREATE TABLE statement (without a trailing semicolon).
type Table struct{ Name, Create string }

// View is a view name with its CREATE VIEW statement (without a trailing semicolon).
type View struct{ Name, Create string }

// StripAutoIncrement removes the table option " AUTO_INCREMENT=<n>".
// The AUTO_INCREMENT keyword in column definitions is kept.
func StripAutoIncrement(createTable string) string {
	return autoIncrementOption.ReplaceAllString(createTable, "")
}

// StripDefiner removes the DEFINER=`u`@`h` clause and one following space.
func StripDefiner(createView string) string {
	return definerClause.ReplaceAllString(createView, "")
}

// OrderViews orders views so that dependencies come first (Kahn's algorithm).
// Ties are broken by name ascending. Dependencies on names outside views are
// ignored. If a cycle exists, the remaining views are appended in name order.
func OrderViews(views []string, deps map[string][]string) []string {
	inSet := make(map[string]bool, len(views))
	for _, v := range views {
		inSet[v] = true
	}
	pending := make([]string, 0, len(inSet))
	for v := range inSet {
		pending = append(pending, v)
	}
	sort.Strings(pending)

	done := make(map[string]bool, len(pending))
	out := make([]string, 0, len(pending))
	for len(pending) > 0 {
		pick := -1
		for i, v := range pending {
			ready := true
			for _, d := range deps[v] {
				if d != v && inSet[d] && !done[d] {
					ready = false
					break
				}
			}
			if ready {
				pick = i
				break
			}
		}
		if pick < 0 {
			// Cycle: append the rest in name order.
			return append(out, pending...)
		}
		v := pending[pick]
		out = append(out, v)
		done[v] = true
		pending = append(pending[:pick], pending[pick+1:]...)
	}
	return out
}

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// Write writes the schema file. createDatabase is the result of
// SHOW CREATE DATABASE; IF NOT EXISTS is inserted into it. Tables are written
// sorted by name, views in the given order.
func Write(w io.Writer, db, createDatabase string, tables []Table, views []View) error {
	var b strings.Builder
	b.WriteString(strings.Replace(createDatabase, "CREATE DATABASE ", "CREATE DATABASE IF NOT EXISTS ", 1))
	b.WriteString(";\n\n")
	b.WriteString(datafile.Header(db))

	sorted := append([]Table(nil), tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, t := range sorted {
		fmt.Fprintf(&b, "\nDROP TABLE IF EXISTS %s;\n%s;\n", quoteIdent(t.Name), t.Create)
	}
	for _, v := range views {
		fmt.Fprintf(&b, "\nDROP VIEW IF EXISTS %s;\n%s;\n", quoteIdent(v.Name), v.Create)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
