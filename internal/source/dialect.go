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
	// defColumns: TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE,
	// IS_NULLABLE, COLUMN_DEFAULT, EXTRA in ordinal order. Param: schema.
	defColumns string
	// defIndexes: TABLE_NAME, INDEX_NAME, NON_UNIQUE, INDEX_TYPE, COLUMN_NAME
	// (NULL for functional key parts), COLLATION ('D' for descending),
	// SUB_PART (set for prefix keys) in SEQ_IN_INDEX order. Param: schema.
	defIndexes string
	// defForeignKeys: TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME,
	// REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME,
	// UPDATE_RULE, DELETE_RULE, one row per column in key order. Param: schema.
	defForeignKeys string
	// defChecks: TABLE_NAME, CONSTRAINT_NAME of CHECK constraints (8.0.16+).
	// Param: schema.
	defChecks string
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
	defColumns: "SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA" +
		" FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?" +
		" ORDER BY TABLE_NAME, ORDINAL_POSITION",
	defIndexes: "SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, INDEX_TYPE, COLUMN_NAME, COLLATION, SUB_PART" +
		" FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ?" +
		" ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX",
	defForeignKeys: "SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME," +
		" k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME," +
		" r.UPDATE_RULE, r.DELETE_RULE" +
		" FROM information_schema.KEY_COLUMN_USAGE k" +
		" JOIN information_schema.REFERENTIAL_CONSTRAINTS r" +
		" ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.TABLE_NAME = k.TABLE_NAME" +
		" AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME" +
		" WHERE k.TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL" +
		" ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION",
	defChecks: "SELECT TABLE_NAME, CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS" +
		" WHERE CONSTRAINT_SCHEMA = ? AND CONSTRAINT_TYPE = 'CHECK'",
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
