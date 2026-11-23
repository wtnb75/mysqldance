package source

import (
	"context"
	"fmt"
	"sort"

	"mysqldance/internal/schemafile"
	"mysqldance/internal/sqlvalue"
)

// Routines returns the stored procedures and functions of the database with
// their SHOW CREATE PROCEDURE / FUNCTION statements: procedures first, then
// functions, each by name in byte order. Routines do not depend on each other
// at creation time (the server does not look up what a body calls), so this
// order is only there to be the same every run.
func (s *Snapshot) Routines(ctx context.Context) (_ []schemafile.Routine, err error) {
	defer func() { err = s.hint(err) }()
	// SHOW_ROUTINE is 8.0.20 and later; the others show a routine on every server;
	// SELECT on mysql.proc is what 5.7 and MariaDB have in place of SHOW_ROUTINE.
	privs := []string{"SHOW_ROUTINE", "CREATE ROUTINE", "ALTER ROUTINE", "EXECUTE"}
	inProcTable, err := s.hasProcTable(ctx)
	if err != nil {
		return nil, err
	}
	if inProcTable {
		privs = append(privs, selectOnProcTable)
	}
	if err := s.requirePrivilege(ctx, "routines", privs...); err != nil {
		return nil, err
	}
	rows, err := s.conn.QueryContext(ctx,
		"SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?", s.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type row struct{ name, typ string }
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.typ); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sorted here, not by the server, so the order does not depend on a collation.
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.typ != b.typ {
			return a.typ == "PROCEDURE"
		}
		return a.name < b.name
	})

	out := make([]schemafile.Routine, 0, len(list))
	for _, r := range list {
		// Another type (MariaDB packages) is not read with a statement we know;
		// refuse rather than leave it out.
		if r.typ != "PROCEDURE" && r.typ != "FUNCTION" {
			return nil, fmt.Errorf("routine %s is a %s, which is not supported", sqlvalue.QuoteIdent(r.name), r.typ)
		}
		mode, create, err := s.showCreateRoutine(ctx, r.typ, r.name)
		if err != nil {
			return nil, err
		}
		out = append(out, schemafile.Routine{Name: r.name, Type: r.typ, Create: create, SQLMode: mode})
	}
	return out, nil
}

// hasProcTable reports whether the server keeps routines in the table
// mysql.proc (MySQL 5.7, MariaDB; 8.x has a data dictionary instead). An account
// that can see the table has some privilege on it. A failed query is an error,
// not a guess: saying yes on 8.x would let SELECT on *.* pass for routines it
// does not list, and saying no would refuse an account that can read them.
func (s *Snapshot) hasProcTable(ctx context.Context) (bool, error) {
	var n int
	err := s.conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'mysql' AND TABLE_NAME = 'proc'").Scan(&n)
	return n > 0, err
}

// showCreateRoutine returns the sql_mode and the statement of SHOW CREATE
// PROCEDURE or FUNCTION.
func (s *Snapshot) showCreateRoutine(ctx context.Context, typ, name string) (mode, create string, err error) {
	stmtCol := "Create Procedure"
	if typ == "FUNCTION" {
		stmtCol = "Create Function"
	}
	return s.showCreateStatement(ctx, typ, name, stmtCol,
		"the SHOW_ROUTINE privilege (or EXECUTE or ALTER ROUTINE on it)")
}
