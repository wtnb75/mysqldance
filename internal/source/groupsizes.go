package source

import (
	"context"
	"fmt"
	"strings"

	"mysqldance/internal/mask"
	"mysqldance/internal/sqlvalue"
)

// GroupSize says that Groups groups of rows are each Size rows large.
type GroupSize struct {
	Size, Groups int64
}

// binaryCompared are the DATA_TYPE values of the columns that are cast to BINARY
// before they are grouped, so the collation cannot make 'A' and 'a' one group.
var binaryCompared = map[string]bool{
	"char": true, "varchar": true,
	"tinytext": true, "text": true, "mediumtext": true, "longtext": true,
	"enum": true, "set": true,
}

func comparesAsBinary(dataType string) bool { return binaryCompared[strings.ToLower(dataType)] }

// groupSizesQuery returns the statement that counts the groups of rows of a
// table that have the same values in cols, by size. A NULL is a value of its own
// (the rows with NULL in a column fall in one group). Only the sizes and how
// many groups have each leave the server, never a value.
func groupSizesQuery(qualifiedTable string, cols []mask.QuasiColumn) string {
	terms := make([]string, len(cols))
	for i, c := range cols {
		terms[i] = sqlvalue.QuoteIdent(c.Name)
		if comparesAsBinary(c.DataType) {
			terms[i] = "CAST(" + terms[i] + " AS BINARY)"
		}
	}
	return "SELECT cnt, COUNT(*) FROM (SELECT COUNT(*) AS cnt FROM " +
		qualifiedTable +
		" GROUP BY " + strings.Join(terms, ", ") + ") g GROUP BY cnt ORDER BY cnt"
}

// GroupSizes groups the rows of the table in the current database by cols, and
// returns how many groups there are of each size, smallest size first. A table
// without rows gives none. The server reads every row and builds a temporary
// table of the groups, so it costs about as much as reading the table.
func (s *Snapshot) GroupSizes(ctx context.Context, table string, cols []mask.QuasiColumn) (_ []GroupSize, err error) {
	defer func() { err = s.hint(err) }()
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s: no column to group by", sqlvalue.QuoteIdent(table))
	}
	rows, err := s.conn.QueryContext(ctx, groupSizesQuery(s.qualified(table), cols))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GroupSize
	for rows.Next() {
		var g GroupSize
		if err := rows.Scan(&g.Size, &g.Groups); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
