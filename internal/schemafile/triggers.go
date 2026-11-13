package schemafile

import (
	"fmt"
	"io"
	"strings"

	"mysqldance/internal/datafile"
	"mysqldance/internal/sqlvalue"
)

// Trigger is a trigger with its CREATE TRIGGER statement (without a trailing
// semicolon) and the sql_mode it was created under. Table is the table it is
// on; it is not written, only reported.
type Trigger struct{ Name, Table, Create, SQLMode string }

// WriteTriggers writes the triggers file: for each trigger, in the given
// order, a DROP, the SET SQL_MODE of its creation (the mode changes how the
// body is parsed), and the CREATE. A body may hold ';', so the statements end
// with a custom delimiter set by the mysql client's DELIMITER command, which is
// what /docker-entrypoint-initdb.d runs the files with.
func WriteTriggers(w io.Writer, db string, triggers []Trigger) error {
	// The creation charset and collation are not kept on purpose: the file runs
	// under the header's SET NAMES utf8mb4, so the output does not depend on the
	// connection that created the trigger (review ledger R-087).
	stmts := make([][]string, len(triggers))
	for i, t := range triggers {
		if hasDelimiterLine(t.Create) {
			return fmt.Errorf("trigger %s has a line that starts with DELIMITER, which the mysql client would run as a command",
				sqlvalue.QuoteIdent(t.Name))
		}
		stmts[i] = dropSetCreate("TRIGGER", t.Name, t.SQLMode, t.Create)
	}
	return writeDelimited(w, db, stmts)
}

// dropSetCreate returns the three statements that re-create one object: the
// DROP, the SET SQL_MODE of its creation, and the CREATE.
func dropSetCreate(kind, name, sqlMode, create string) []string {
	return []string{
		"DROP " + kind + " IF EXISTS " + sqlvalue.QuoteIdent(name),
		"SET SQL_MODE=" + sqlvalue.Literal("VARCHAR", []byte(sqlMode), false),
		create,
	}
}

// writeDelimited writes the header, then each group of statements (a blank line
// before each group), every statement ended by one delimiter that none of them
// contains.
func writeDelimited(w io.Writer, db string, groups [][]string) error {
	var flat []string
	for _, ss := range groups {
		flat = append(flat, ss...)
	}
	delim := chooseDelimiter(flat)

	var b strings.Builder
	b.WriteString(datafile.Header(db))
	b.WriteString("DELIMITER " + delim + "\n")
	for _, ss := range groups {
		b.WriteString("\n")
		for _, s := range ss {
			fmt.Fprintf(&b, "%s%s\n", s, delim)
		}
	}
	b.WriteString("\nDELIMITER ;\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// hasDelimiterLine reports whether a line of s starts with the client command
// DELIMITER (in any case), which the client would run even inside a statement.
func hasDelimiterLine(s string) bool {
	const word = "DELIMITER"
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimLeft(line, " \t")
		if len(line) >= len(word) && strings.EqualFold(line[:len(word)], word) {
			return true
		}
	}
	return false
}

// chooseDelimiter returns the first of "$$", "//", "##", "%%", "!!", "$$$", ...
// that ends every statement unambiguously. The client cuts at the first
// occurrence, so the delimiter must not appear inside a statement, nor be
// formed together with the statement's last characters (`SET @a$` followed by
// `$$` reads as `SET @a` and a stray `$`). It depends on the statements alone,
// so the output stays deterministic.
func chooseDelimiter(stmts []string) string {
	for n := 2; ; n++ {
		for _, c := range "$/#%!" {
			d := strings.Repeat(string(c), n)
			ok := true
			for _, s := range stmts {
				if strings.Index(s+d, d) != len(s) {
					ok = false
					break
				}
			}
			if ok {
				return d
			}
		}
	}
}
