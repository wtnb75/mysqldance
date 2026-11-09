package source

import (
	"fmt"
	"strconv"
	"strings"
)

// dialect collects every catalog query whose shape depends on the server
// version, so support for other versions (e.g. 5.7, which lacks
// VIEW_TABLE_USAGE and INVISIBLE columns) can be added here in one place.
//
// Each query takes the target schema name as its parameter(s) and returns
// the columns documented next to it.
type dialect struct {
	// tables: TABLE_NAME, TABLE_TYPE, ENGINE, TABLE_ROWS. Param: schema.
	tables string
	// columns: TABLE_NAME, COLUMN_NAME, DATA_TYPE, IS_NULLABLE, EXTRA, in
	// ordinal order. EXTRA contains "VIRTUAL GENERATED" / "STORED GENERATED"
	// for generated columns and "INVISIBLE" (8.0.23+) for invisible ones.
	// Param: schema.
	columns string
	// uniqueKeys: TABLE_NAME, INDEX_NAME, COLUMN_NAME (NULL for functional
	// key parts, 8.0.13+) for unique indexes, in SEQ_IN_INDEX order.
	// Param: schema.
	uniqueKeys string
	// viewDeps: VIEW_NAME, TABLE_NAME of objects in the same schema that a
	// view references (VIEW_TABLE_USAGE, 8.0.13+). Params: schema, schema.
	viewDeps string
}

var mysql8 = dialect{
	tables: "SELECT TABLE_NAME, TABLE_TYPE, ENGINE, TABLE_ROWS" +
		" FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?",
	columns: "SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, IS_NULLABLE, EXTRA" +
		" FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?" +
		" ORDER BY TABLE_NAME, ORDINAL_POSITION",
	uniqueKeys: "SELECT TABLE_NAME, INDEX_NAME, COLUMN_NAME" +
		" FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND NON_UNIQUE = 0" +
		" ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX",
	viewDeps: "SELECT VIEW_NAME, TABLE_NAME" +
		" FROM information_schema.VIEW_TABLE_USAGE WHERE VIEW_SCHEMA = ? AND TABLE_SCHEMA = ?",
}

// dialectFor picks the dialect for a server reporting SELECT VERSION().
// Only MySQL 8.0 and later are supported.
func dialectFor(version string) (dialect, error) {
	if strings.Contains(strings.ToLower(version), "mariadb") {
		return dialect{}, fmt.Errorf("unsupported server version %q: MariaDB is not supported", version)
	}
	major, _, _ := strings.Cut(version, ".")
	if n, err := strconv.Atoi(major); err != nil || n < 8 {
		return dialect{}, fmt.Errorf("unsupported server version %q: MySQL 8.0 or later is required", version)
	}
	return mysql8, nil
}
