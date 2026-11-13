package source

import (
	"context"
	"sort"

	"mysqldance/internal/schemafile"
)

// Events returns the events of the database with their SHOW CREATE EVENT
// statements, sql_mode and time zone, by name in byte order. An event does not
// depend on another, so the order is only there to be the same every run.
func (s *Snapshot) Events(ctx context.Context) ([]schemafile.Event, error) {
	rows, err := s.conn.QueryContext(ctx,
		"SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?", s.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sorted here, not by the server, so the order does not depend on a collation.
	sort.Strings(names)

	out := make([]schemafile.Event, 0, len(names))
	for _, n := range names {
		cols, err := s.showCreateColumns(ctx, "EVENT", n, "Create Event", "the EVENT privilege")
		if err != nil {
			return nil, err
		}
		out = append(out, schemafile.Event{
			Name: n, Create: cols["Create Event"], SQLMode: cols["sql_mode"], TimeZone: cols["time_zone"],
		})
	}
	return out, nil
}
