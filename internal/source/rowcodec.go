package source

import (
	"encoding/binary"
	"errors"

	"mysqldance/internal/sqlvalue"
)

// appendSortKey appends the sort key of a row of a table without a key to dst:
// the MySQL literals of its cells joined with commas. Rows are ordered by this
// text in byte order, which does not depend on collations or the server.
// Rendering each cell to a string here and again in the writer is accepted
// (review ledger R-045).
func appendSortKey(dst []byte, row []sqlvalue.Value) []byte {
	for i, v := range row {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, v.Literal()...)
	}
	return dst
}

// encodeRow appends row to dst: per cell, 0 for NULL, else the length plus one
// and the bytes. The types are not stored; decodeRow takes them from the
// columns.
func encodeRow(dst []byte, row []sqlvalue.Value) []byte {
	for _, v := range row {
		if v.IsNull() {
			dst = binary.AppendUvarint(dst, 0)
			continue
		}
		b := v.Bytes()
		dst = binary.AppendUvarint(dst, uint64(len(b))+1)
		dst = append(dst, b...)
	}
	return dst
}

var errBadRow = errors.New("corrupt sorted row")

// decodeRow reads a row written by encodeRow for cols. NewValue copies the bytes, so the
// values do not share memory with b.
func decodeRow(cols []sqlvalue.Column, b []byte) ([]sqlvalue.Value, error) {
	row := make([]sqlvalue.Value, len(cols))
	for i, c := range cols {
		n, w := binary.Uvarint(b)
		if w <= 0 {
			return nil, errBadRow
		}
		b = b[w:]
		if n == 0 {
			row[i] = sqlvalue.NewValue(c.TypeName, nil, true)
			continue
		}
		n--
		if n > uint64(len(b)) {
			return nil, errBadRow
		}
		row[i] = sqlvalue.NewValue(c.TypeName, b[:n], false)
		b = b[n:]
	}
	if len(b) != 0 {
		return nil, errBadRow
	}
	return row, nil
}
