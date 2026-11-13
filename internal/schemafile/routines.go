package schemafile

import (
	"fmt"
	"io"
	"strings"

	"mysqldance/internal/sqlvalue"
)

// Routine is a stored procedure or function with its CREATE statement (without
// a trailing semicolon) and the sql_mode it was created under. Type is
// "PROCEDURE" or "FUNCTION".
type Routine struct{ Name, Type, Create, SQLMode string }

// WriteRoutines writes the routines file: for each routine, in the given order,
// a DROP, the SET SQL_MODE of its creation (the mode changes how the body is
// parsed), and the CREATE. A body holds ';', so the statements end with a
// custom delimiter, as in the triggers file (see WriteTriggers). A type other
// than PROCEDURE and FUNCTION is an error rather than left out.
func WriteRoutines(w io.Writer, db string, routines []Routine) error {
	// The creation charset, collation and database collation are not kept, as
	// for triggers (review ledger R-087): the file runs under the header's
	// SET NAMES utf8mb4.
	stmts := make([][]string, len(routines))
	for i, r := range routines {
		if r.Type != "PROCEDURE" && r.Type != "FUNCTION" {
			return fmt.Errorf("routine %s is a %s, which is not output", sqlvalue.QuoteIdent(r.Name), r.Type)
		}
		if hasDelimiterLine(r.Create) {
			return fmt.Errorf("%s %s has a line that starts with DELIMITER, which the mysql client would run as a command",
				strings.ToLower(r.Type), sqlvalue.QuoteIdent(r.Name))
		}
		stmts[i] = dropSetCreate(r.Type, r.Name, r.SQLMode, r.Create)
	}
	return writeDelimited(w, db, stmts)
}
