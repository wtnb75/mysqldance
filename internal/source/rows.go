package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"mysqldance/internal/extsort"
	"mysqldance/internal/sqlvalue"
)

// stringKeyTypes are the DATA_TYPE values of key columns compared as binary
// in ORDER BY, so collations cannot change the order. Values distinct under a
// unique key's collation always differ in bytes, so the order stays total.
// ENUM/SET sort by their member index, which is already collation-independent.
var stringKeyTypes = map[string]bool{
	"char": true, "varchar": true,
	"tinytext": true, "text": true, "mediumtext": true, "longtext": true,
}

// Rows returns the rows of t as typed values, one []sqlvalue.Value per row in
// t.Columns order, collected from EachRow. It holds every row in memory; dump
// uses EachRow instead.
func (s *Snapshot) Rows(ctx context.Context, t TableInfo) ([][]sqlvalue.Value, error) {
	var out [][]sqlvalue.Value
	err := s.EachRow(ctx, t, func(row []sqlvalue.Value) error {
		out = append(out, row)
		return nil
	})
	return out, err
}

// EachRow calls fn with each row of t in output order, one []sqlvalue.Value per
// row in t.Columns order. With a key the server orders the rows and they are
// passed on as they arrive, so memory does not grow with the table; otherwise
// all rows are read first and sorted by their MySQL literals joined with commas,
// in byte order: in memory, or, with Config.SortFile, through scratch files
// once the rows held reach its limit (all rows are read before the first is
// passed on either way). fn owns the row it is given. If fn fails, the
// read stops and EachRow returns its error. A read that ends early (a lost
// connection, a canceled context) is an error too, never a short table.
// t must come from Tables for the database currently selected by Use.
func (s *Snapshot) EachRow(ctx context.Context, t TableInfo, fn func(row []sqlvalue.Value) error) (err error) {
	orderBy, err := orderByClause(t.OrderBy, t.OrderByTypes)
	if err != nil {
		return fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
	}
	query := "SELECT " + selectList(t.Columns) + " FROM " + s.qualified(t.Name) + orderBy

	// No query arguments: the text protocol delivers values as the server
	// formats them, and sql.RawBytes keeps them unconverted.
	rows, err := s.conn.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	// Closing after an early return reads the rest of the result off the wire:
	// the abort lasts up to one table scan, but the connection (and its snapshot)
	// stays usable (review ledger R-084).
	defer func() { _ = rows.Close() }()
	raw := make([]sql.RawBytes, len(t.Columns))
	dest := make([]any, len(raw))
	isFloat := make([]bool, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
		isFloat[i] = sqlvalue.IsFloat(t.Columns[i].TypeName)
	}

	emit := fn
	var srt *extsort.Sorter
	if len(t.OrderBy) == 0 {
		// A table without a key is ordered by the text of its rows. Each row is
		// added to a sorter (which spills to scratch files with Config.SortFile)
		// and played back in order after the read. Rendering the literals here and
		// again in the writer is accepted (review ledger R-045).
		srt = extsort.New(s.sortMemory, s.sortFile)
		defer func() { err = errors.Join(err, srt.Close()) }()
		var key, val []byte
		emit = func(row []sqlvalue.Value) error {
			key, val = appendSortKey(key[:0], row), encodeRow(val[:0], row)
			return srt.Add(key, val)
		}
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		row := make([]sqlvalue.Value, len(raw))
		for i, b := range raw {
			if b != nil && isFloat[i] {
				if b, err = sqlvalue.ExactFloat(b); err != nil {
					return fmt.Errorf("table %s, column %s: %w", sqlvalue.QuoteIdent(t.Name),
						sqlvalue.QuoteIdent(t.Columns[i].Name), err)
				}
			}
			row[i] = sqlvalue.NewValue(t.Columns[i].TypeName, b, b == nil)
		}
		if err := emit(row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if srt == nil {
		return nil
	}
	return srt.Each(func(_, val []byte) error {
		row, err := decodeRow(t.Columns, val)
		if err != nil {
			return fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
		}
		return fn(row)
	})
}

// orderByClause returns the ORDER BY clause (with a leading space) for the
// key columns whose DATA_TYPEs are types, or "" for no key. It fails when a
// key column has no type: guessing would sort a string key by collation and
// make the output order depend on it.
func orderByClause(key, types []string) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	if len(types) != len(key) {
		return "", fmt.Errorf("key has %d columns but %d types", len(key), len(types))
	}
	terms := make([]string, len(key))
	for i, c := range key {
		if types[i] == "" {
			return "", fmt.Errorf("key column %s has no data type", sqlvalue.QuoteIdent(c))
		}
		terms[i] = sqlvalue.QuoteIdent(c)
		if stringKeyTypes[types[i]] {
			terms[i] = "CAST(" + terms[i] + " AS BINARY)"
		}
	}
	return " ORDER BY " + strings.Join(terms, ", "), nil
}

func (s *Snapshot) qualified(name string) string {
	return sqlvalue.QuoteIdent(s.name) + "." + sqlvalue.QuoteIdent(name)
}

// selectList returns the select list that reads cols. The text protocol prints
// a FLOAT with 6 significant digits, which loses bits, so a FLOAT is read as
// `col * 1e0`: its exact value as a DOUBLE, which keeps -0 too (B-059). It does
// not use CAST(... AS DOUBLE), which MySQL 5.7 and 8.0 before 8.0.17 lack.
// EachRow turns that text back into the FLOAT's own notation.
func selectList(cols []sqlvalue.Column) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = sqlvalue.QuoteIdent(c.Name)
		if sqlvalue.IsFloat(c.TypeName) {
			q[i] += " * 1e0"
		}
	}
	return strings.Join(q, ", ")
}

func columnList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = sqlvalue.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}
