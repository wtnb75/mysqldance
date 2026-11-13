package schemafile

import (
	"fmt"
	"io"

	"mysqldance/internal/sqlvalue"
)

// Event is a scheduled event with its CREATE EVENT statement (without a
// trailing semicolon), the sql_mode it was created under, and its time zone as
// SHOW CREATE EVENT reports it ("SYSTEM", an offset such as "+09:00", or a
// name).
type Event struct{ Name, Create, SQLMode, TimeZone string }

// WriteEvents writes the events file: for each event, in the given order, a
// DROP, the SET SQL_MODE and SET TIME_ZONE of its creation, and the CREATE. The
// mode changes how the body is parsed, and the server keeps the session's time
// zone with the event: its schedule (AT, STARTS, ENDS) is read in that zone and
// the body runs in it, so the header's zone would change when it fires. A body
// may hold ';', so the statements end with a custom delimiter, as in the
// triggers file (see WriteTriggers).
//
// The status (ENABLE, DISABLE, DISABLE ON SLAVE) stays as the statement has it,
// so an enabled event runs once the restored server's scheduler is on (see
// README "Events").
func WriteEvents(w io.Writer, db string, events []Event) error {
	// The creation charset and collation are not kept, as for triggers (review
	// ledger R-087). The time zone is not set back after the last event: nothing
	// follows in this file and every file starts with its own header (R-170).
	stmts := make([][]string, len(events))
	for i, e := range events {
		if hasDelimiterLine(e.Create) {
			return fmt.Errorf("event %s has a line that starts with DELIMITER, which the mysql client would run as a command",
				sqlvalue.QuoteIdent(e.Name))
		}
		if e.TimeZone == "" {
			return fmt.Errorf("event %s has no time zone, so the zone it runs in cannot be restored", sqlvalue.QuoteIdent(e.Name))
		}
		s := dropSetCreate("EVENT", e.Name, e.SQLMode, e.Create)
		stmts[i] = []string{s[0], s[1], "SET TIME_ZONE=" + sqlvalue.Literal("VARCHAR", []byte(e.TimeZone), false), s[2]}
	}
	return writeDelimited(w, db, stmts)
}
