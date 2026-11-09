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
	// Anchored to the statement header so view bodies are never touched.
	definerClause = regexp.MustCompile("^(CREATE (?:OR REPLACE )?(?:ALGORITHM=\\w+ )?)DEFINER=" + ident + "@" + ident + " ?")
)

// Table is a table name with its CREATE TABLE statement (without a trailing semicolon).
type Table struct{ Name, Create string }

// View is a view name with its CREATE VIEW statement (without a trailing semicolon).
type View struct{ Name, Create string }

// createParts locates the pieces of a SHOW CREATE TABLE statement: the
// top-level items of the definition list (columns, keys, constraints) and the
// byte range [optStart, optEnd) of the table options that follow its closing
// parenthesis, up to the end of that line. ok is false if s cannot be parsed.
//
// The scan skips backtick identifiers (printed raw, so they may hold newlines
// and parentheses) and single- or double-quoted strings (backslash escapes,
// as printed under an empty sql_mode). Strings inside the definition list and the
// table options are escaped, so the first newline after the closing
// parenthesis ends the options; partition clauses come after it and may
// contain raw newlines in partition COMMENTs.
func createParts(s string) (items []string, optStart, optEnd int, ok bool) {
	depth, itemStart := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '`', '\'', '"':
			end := skipQuoted(s, i)
			if end < 0 {
				return nil, 0, 0, false
			}
			i = end
		case '(':
			depth++
			if depth == 1 {
				itemStart = i + 1
			}
		case ',':
			if depth == 1 {
				items = append(items, s[itemStart:i])
				itemStart = i + 1
			}
		case ')':
			if depth == 0 {
				return nil, 0, 0, false
			}
			depth--
			if depth == 0 {
				items = append(items, s[itemStart:i])
				optStart = i + 1
				optEnd = strings.IndexByte(s[optStart:], '\n')
				if optEnd < 0 {
					optEnd = len(s)
				} else {
					optEnd += optStart
				}
				return items, optStart, optEnd, true
			}
		}
	}
	return nil, 0, 0, false
}

// skipQuoted returns the index of the quote closing the quoted token that
// starts at s[i], or -1 if it is unterminated. A doubled quote continues the
// token; inside strings a backslash escapes the next byte.
func skipQuoted(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if q != '`' {
				j++
			}
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j
		}
	}
	return -1
}

// StripAutoIncrement removes the table option " AUTO_INCREMENT=<n>".
// The AUTO_INCREMENT keyword in column definitions is kept, and so is any
// such text in identifiers, strings, comments and partition clauses.
// Input that cannot be parsed is returned unchanged.
func StripAutoIncrement(createTable string) string {
	_, start, end, ok := createParts(createTable)
	if !ok {
		return createTable
	}
	// Only the options before the first quote are touched, so COMMENT text
	// is kept (AUTO_INCREMENT always precedes COMMENT).
	opts := createTable[start:end]
	if q := strings.IndexByte(opts, '\''); q >= 0 {
		opts = opts[:q]
	}
	loc := autoIncrementOption.FindStringIndex(opts)
	if loc == nil {
		return createTable
	}
	return createTable[:start+loc[0]] + createTable[start+loc[1]:]
}

// ColumnNames returns the names of all columns, generated and INVISIBLE ones
// included, defined by a SHOW CREATE TABLE statement, in definition order.
// It requires identifiers quoted with backticks (sql_quote_show_create=1 and
// no ANSI_QUOTES): items not starting with a backtick are keys and constraints.
func ColumnNames(createTable string) ([]string, error) {
	items, _, _, ok := createParts(createTable)
	if !ok {
		return nil, fmt.Errorf("cannot parse CREATE TABLE statement: %q", createTable)
	}
	var names []string
	for _, item := range items {
		item = strings.TrimLeft(item, " \t\n")
		if !strings.HasPrefix(item, "`") {
			continue
		}
		end := skipQuoted(item, 0)
		if end < 0 {
			return nil, fmt.Errorf("cannot parse column definition %q", item)
		}
		names = append(names, strings.ReplaceAll(item[1:end], "``", "`"))
	}
	return names, nil
}

// StripDefiner removes the DEFINER=`u`@`h` clause and one following space.
func StripDefiner(createView string) string {
	return definerClause.ReplaceAllString(createView, "$1")
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
// SHOW CREATE DATABASE; IF NOT EXISTS is inserted into it. It goes between
// the header's settings (so SET NAMES applies to the database name) and its
// USE statement. Tables are written sorted by name, views in the given order.
func Write(w io.Writer, db, createDatabase string, tables []Table, views []View) error {
	var b strings.Builder
	b.WriteString(datafile.HeaderSettings())
	b.WriteString("\n")
	b.WriteString(strings.Replace(createDatabase, "CREATE DATABASE ", "CREATE DATABASE IF NOT EXISTS ", 1))
	b.WriteString(";\n")
	b.WriteString(datafile.HeaderUse(db))

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
