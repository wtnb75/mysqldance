package source

import (
	"context"
	"database/sql"
	"strings"

	"mysqldance/internal/sqlvalue"
)

// EachCells calls fn with the cells of the named columns of each row of a table
// of the current database, as the text the server returns (nil for NULL). It is
// the read of a scan: the rows are passed on in the order the server sends them
// and are never sorted, so nothing is held back and nothing goes to a scratch
// file (EachRow puts the rows of a table without a key through extsort, which
// writes them to a file once the memory is full). Only the named columns are
// selected, so memory does not grow with the table.
//
// The cells are valid until fn returns; fn must copy what it keeps. If fn fails,
// the read stops and EachCells returns its error. A read that ends early is an
// error, never a short table. The table must be one of Tables for the database
// currently selected by Use.
func (s *Snapshot) EachCells(ctx context.Context, table string, columns []string, fn func(cells []sql.RawBytes) error) (err error) {
	defer func() { err = s.hint(err) }()
	// No query arguments: the text protocol delivers the values as the server
	// formats them, and sql.RawBytes keeps them unconverted.
	rows, err := s.conn.QueryContext(ctx, cellsQuery(s.qualified(table), columns))
	if err != nil {
		return err
	}
	// Closing after an early return reads the rest of the result off the wire,
	// but the connection (and its snapshot) stays usable.
	defer func() { _ = rows.Close() }()
	return s.eachCellsOf(rows, len(columns), fn)
}

// cellsQuery returns the statement that reads columns of a table. It has no
// ORDER BY: the order of the rows is of no use to a scan.
func cellsQuery(qualifiedTable string, columns []string) string {
	terms := make([]string, len(columns))
	for i, c := range columns {
		terms[i] = sqlvalue.QuoteIdent(c)
	}
	return "SELECT " + strings.Join(terms, ", ") + " FROM " + qualifiedTable
}

// eachCellsOf is the part of EachCells after the query.
func (s *Snapshot) eachCellsOf(rows rowScanner, n int, fn func(cells []sql.RawBytes) error) error {
	cells := make([]sql.RawBytes, n)
	dest := make([]any, n)
	for i := range cells {
		dest[i] = &cells[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		if err := fn(cells); err != nil {
			return err
		}
	}
	return rows.Err()
}
