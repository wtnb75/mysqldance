package source

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"mysqldance/internal/schemafile"
	"mysqldance/internal/sqlvalue"
)

// Triggers returns the triggers of the database with their SHOW CREATE TRIGGER
// statements, ordered by table, then timing and event, then the order the
// server runs them in (the order they were created in). Triggers of the same
// table, timing and event keep that relative order, so a restore that creates
// them in this order runs them as the source does; the rest is name order.
func (s *Snapshot) Triggers(ctx context.Context) ([]schemafile.Trigger, error) {
	rows, err := s.conn.QueryContext(ctx,
		"SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE, ACTION_TIMING, EVENT_MANIPULATION, ACTION_ORDER"+
			" FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?", s.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type row struct {
		name, table, timing, event string
		order                      int64
	}
	var list []row
	for rows.Next() {
		var r row
		var order sql.NullInt64
		if err := rows.Scan(&r.name, &r.table, &r.timing, &r.event, &order); err != nil {
			return nil, err
		}
		// Without the order, triggers of one event would fall back to name order
		// and run in another order after a restore; refuse instead of guessing.
		if !order.Valid {
			return nil, fmt.Errorf("trigger %s has no ACTION_ORDER in information_schema.TRIGGERS", sqlvalue.QuoteIdent(r.name))
		}
		r.order = order.Int64
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sorted here, not by the server, so the order does not depend on a collation.
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch {
		case a.table != b.table:
			return a.table < b.table
		case a.timing != b.timing:
			return a.timing < b.timing
		case a.event != b.event:
			return a.event < b.event
		case a.order != b.order:
			return a.order < b.order
		}
		return a.name < b.name
	})

	out := make([]schemafile.Trigger, 0, len(list))
	for _, r := range list {
		mode, create, err := s.showCreateTrigger(ctx, r.name)
		if err != nil {
			return nil, err
		}
		out = append(out, schemafile.Trigger{Name: r.name, Table: r.table, Create: create, SQLMode: mode})
	}
	return out, nil
}

// showCreateTrigger returns the sql_mode and the statement of SHOW CREATE
// TRIGGER.
func (s *Snapshot) showCreateTrigger(ctx context.Context, name string) (mode, create string, err error) {
	return s.showCreateStatement(ctx, "TRIGGER", name, "SQL Original Statement", "the TRIGGER privilege")
}

// showCreateStatement returns the sql_mode and the statement of SHOW CREATE
// <kind> for a trigger or a routine. The columns are picked by name: the
// statement is the third one here, not the second as for tables and views.
// needs names what the account must have, for the error when the statement is
// NULL. kind is joined into the SQL, so callers pass a constant (R-168).
func (s *Snapshot) showCreateStatement(ctx context.Context, kind, name, stmtCol, needs string) (mode, create string, err error) {
	rows, err := s.conn.QueryContext(ctx, "SHOW CREATE "+kind+" "+s.qualified(name))
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return "", "", err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("SHOW CREATE %s %s returned no rows", kind, sqlvalue.QuoteIdent(name))
	}
	vals := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range vals {
		dest[i] = &vals[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return "", "", err
	}
	modeIdx, stmtIdx := -1, -1
	for i, c := range cols {
		switch c {
		case "sql_mode":
			modeIdx = i
		case stmtCol:
			stmtIdx = i
		}
	}
	if modeIdx < 0 || stmtIdx < 0 {
		return "", "", fmt.Errorf("SHOW CREATE %s %s has the columns %q, not the expected sql_mode and %s",
			kind, sqlvalue.QuoteIdent(name), cols, stmtCol)
	}
	// The statement is NULL when the account may not see the body.
	if vals[stmtIdx] == nil {
		return "", "", fmt.Errorf("SHOW CREATE %s %s returned no statement; the account needs %s",
			kind, sqlvalue.QuoteIdent(name), needs)
	}
	return string(vals[modeIdx]), string(vals[stmtIdx]), rows.Err()
}
