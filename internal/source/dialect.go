package source

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"mysqldance/internal/sqlvalue"
)

// dialect collects everything that depends on the kind and version of the
// server: the catalog queries, the session settings, and how some of their
// results are read. Support for another server (MariaDB) or version (5.7 and 5.6, which
// lack VIEW_TABLE_USAGE and INVISIBLE columns) is added here, in one place;
// no other code branches on the server.
//
// Each query takes the target schema name as its parameter(s) and returns
// the columns documented next to it.
type dialect struct {
	// session lists the statements that pin the session settings changing
	// SHOW CREATE output or value text. They run before the snapshot starts.
	session []string
	// tables: TABLE_NAME, TABLE_TYPE, ENGINE, TABLE_ROWS. Param: schema.
	tables string
	// columns: TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, EXTRA, in
	// ordinal order. EXTRA contains "VIRTUAL GENERATED" / "STORED GENERATED"
	// for generated columns and "INVISIBLE" (8.0.23+) for invisible ones.
	// Param: schema.
	columns string
	// uniqueKeys: TABLE_NAME, INDEX_NAME, COLUMN_NAME (NULL for functional
	// key parts, 8.0.13+) for unique indexes, in SEQ_IN_INDEX order.
	// Param: schema.
	uniqueKeys string
	// viewDeps returns the objects of schema that each of views references,
	// as (view, referenced name) pairs. Names not in views may be returned too;
	// the caller keeps the views only.
	viewDeps func(ctx context.Context, conn *sql.Conn, schema string, views []string) ([]viewDep, error)
	// defColumns: TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE,
	// IS_NULLABLE, COLUMN_DEFAULT, EXTRA in ordinal order. Param: schema.
	defColumns string
	// columnDefault reads COLUMN_DEFAULT (with the DATA_TYPE and EXTRA of the
	// same column) as the default value, unquoted, or nil when the column has
	// none; expr reports that the text is a default expression, not a value.
	columnDefault func(raw sql.NullString, dataType, extra string) (def *string, expr bool)
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

// viewDep says that a view references an object of the same schema.
type viewDep struct{ view, ref string }

// Session settings both servers have.
var (
	sessionPrelude  = []string{"SET SESSION sql_mode = ''", "SET SESSION sql_quote_show_create = 1"}
	sessionPostlude = []string{
		"SET time_zone = '+00:00'",
		"SET NAMES utf8mb4",
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
	}
	// The servers without show_create_table_verbosity (MariaDB, MySQL 5.7 and 5.6).
	sessionNoVerbosity = slices.Concat(sessionPrelude, sessionPostlude)
)

var mysql8 = dialect{
	session: slices.Concat(sessionPrelude, []string{"SET SESSION show_create_table_verbosity = 0"}, sessionPostlude),
	tables: "SELECT TABLE_NAME, TABLE_TYPE, ENGINE, TABLE_ROWS" +
		" FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?",
	columns: "SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, EXTRA" +
		" FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?" +
		" ORDER BY TABLE_NAME, ORDINAL_POSITION",
	uniqueKeys: "SELECT TABLE_NAME, INDEX_NAME, COLUMN_NAME" +
		" FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND NON_UNIQUE = 0" +
		" ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX",
	viewDeps: viewTableUsage,
	defColumns: "SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA" +
		" FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?" +
		" ORDER BY TABLE_NAME, ORDINAL_POSITION",
	columnDefault: mysqlColumnDefault,
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

// mariadb is MariaDB (checked on 10.11 and 11.4). Every catalog query of
// mysql8 works unchanged and returns the same columns; what differs is the
// session variables, the lack of VIEW_TABLE_USAGE, and the meaning of
// COLUMN_DEFAULT. SHOW CREATE output keeps MariaDB's own form (see the design
// document): a dump of MariaDB is for restoring into MariaDB.
var mariadb = func() dialect {
	d := mysql8
	d.session = sessionNoVerbosity
	d.viewDeps = viewDefinitions
	d.columnDefault = mariadbColumnDefault
	return d
}()

// mysql57 is MySQL 5.7 (checked on 5.7.44) and MySQL 5.6 (checked on 5.6.51;
// the queries and their results are the same). The catalog queries of mysql8
// work unchanged; what differs is the lack of VIEW_TABLE_USAGE, of
// show_create_table_verbosity before 5.7.22, and of DEFAULT_GENERATED in EXTRA.
// SHOW CREATE output keeps its 5.7 form (int(11), latin1): a dump of 5.7 is
// for restoring into 5.7 or 8.x as it is.
var mysql57 = func() dialect {
	d := mysql8
	// 5.7.21 and earlier have no show_create_table_verbosity, and the version
	// is not told apart: 5.7 never sets it (a decided spec, see R-069 in
	// docs/review-decisions.md; the README says the output follows a global ON).
	d.session = sessionNoVerbosity
	d.viewDeps = viewDefinitions
	d.columnDefault = mysql57ColumnDefault
	return d
}()

// dialectFor picks the dialect for a server reporting SELECT VERSION().
// MySQL 5.6 (5.6.51 was checked), MySQL 5.7 (5.7.44 was checked), MySQL 8.0 and
// later, and MariaDB are supported; MariaDB is not rejected by version (10.11
// and 11.4 were checked).
func dialectFor(version string) (dialect, error) {
	if strings.Contains(strings.ToLower(version), "mariadb") {
		return mariadb, nil
	}
	// "8.0.46-0ubuntu0.22.04.1" and "5.7.44-log" read as 8 and 5.7.
	var major, minor int
	n, _ := fmt.Sscanf(version, "%d.%d", &major, &minor)
	switch {
	case n >= 1 && major >= 8:
		return mysql8, nil
	// 5.6 reads as 5.7 does (the design document, section 11, B-079).
	case n == 2 && major == 5 && (minor == 6 || minor == 7):
		return mysql57, nil
	}
	return dialect{}, fmt.Errorf("unsupported server version %q: MySQL 5.6 or later (or MariaDB) is required", version)
}

// viewTableUsage reads the references of views from VIEW_TABLE_USAGE (8.0.13+).
func viewTableUsage(ctx context.Context, conn *sql.Conn, schema string, _ []string) ([]viewDep, error) {
	rows, err := conn.QueryContext(ctx, "SELECT VIEW_NAME, TABLE_NAME"+
		" FROM information_schema.VIEW_TABLE_USAGE WHERE VIEW_SCHEMA = ? AND TABLE_SCHEMA = ?", schema, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []viewDep
	for rows.Next() {
		var d viewDep
		if err := rows.Scan(&d.view, &d.ref); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// viewDefinitions finds the references of views in VIEW_DEFINITION, for a
// server without VIEW_TABLE_USAGE. A server shows the definition with every
// table and view qualified by its schema, so a reference is `schema`.`name`.
func viewDefinitions(ctx context.Context, conn *sql.Conn, schema string, views []string) ([]viewDep, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT TABLE_NAME, VIEW_DEFINITION FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ?", schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	defs := map[string]string{}
	for rows.Next() {
		var name string
		var def sql.NullString
		if err := rows.Scan(&name, &def); err != nil {
			return nil, err
		}
		// An account without SHOW VIEW sees an empty definition. Reading it as
		// "no references" would silently order the views wrongly.
		if !def.Valid || def.String == "" {
			return nil, fmt.Errorf("the definition of view %s is not visible (SHOW VIEW privilege)", sqlvalue.QuoteIdent(name))
		}
		defs[name] = def.String
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return viewRefs(defs, schema, views), nil
}

// viewRefs returns which of views each definition in defs (view name ->
// VIEW_DEFINITION) references, in the order of views. A view
// does not reference itself. Names compare without regard to case (a server
// with lower_case_table_names=2 may spell them differently from TABLE_NAME),
// and a name inside a string literal would count too: both only add an
// ordering constraint, and a cycle they might form is not an error.
func viewRefs(defs map[string]string, schema string, views []string) []viewDep {
	var out []viewDep
	for _, v := range views {
		def := strings.ToLower(defs[v])
		for _, ref := range views {
			if ref != v && strings.Contains(def, strings.ToLower(sqlvalue.QuoteIdent(schema)+"."+sqlvalue.QuoteIdent(ref))) {
				out = append(out, viewDep{v, ref})
			}
		}
	}
	return out
}

// mysqlColumnDefault: COLUMN_DEFAULT is the value itself, unquoted, or NULL;
// an expression default is flagged by DEFAULT_GENERATED in EXTRA.
func mysqlColumnDefault(raw sql.NullString, _, extra string) (*string, bool) {
	if !raw.Valid {
		return nil, false
	}
	return &raw.String, strings.Contains(strings.ToLower(extra), "default_generated")
}

// mysql57ColumnDefault: as mysqlColumnDefault, but EXTRA has no
// DEFAULT_GENERATED. The only default expression 5.7 has is
// CURRENT_TIMESTAMP[(n)] of a TIMESTAMP or DATETIME column; the same text in a
// string column is a string.
func mysql57ColumnDefault(raw sql.NullString, dataType, _ string) (*string, bool) {
	if !raw.Valid {
		return nil, false
	}
	switch strings.ToLower(dataType) {
	case "timestamp", "datetime":
		return &raw.String, strings.HasPrefix(strings.ToUpper(raw.String), "CURRENT_TIMESTAMP")
	}
	return &raw.String, false
}

var numericLiteral = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// mariadbColumnDefault: COLUMN_DEFAULT is the text of the DEFAULT clause. SQL
// NULL means no default, the text NULL is DEFAULT NULL (a default of the
// string NULL is 'NULL', quoted), a quoted string is an escaped literal, a
// number or a bit string b'...' is a value, and anything else is an
// expression (current_timestamp(), uuid(), (`a` + 1)). EXTRA says nothing
// about it.
func mariadbColumnDefault(raw sql.NullString, _, _ string) (*string, bool) {
	if !raw.Valid || raw.String == "NULL" {
		return nil, false
	}
	s := raw.String
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		v := unquoteString(s[1 : len(s)-1])
		return &v, false
	}
	if numericLiteral.MatchString(s) || (strings.HasPrefix(s, "b'") && strings.HasSuffix(s, "'")) {
		return &s, false
	}
	return &s, true
}

// unquoteString undoes the escaping of a quoted string literal: a doubled
// quote and the backslash escapes of MySQL (\0 \b \n \r \t \Z, and \x for any
// other x). The \% and \_ escapes keep their backslash, as they do in a
// pattern.
func unquoteString(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && i+1 < len(s) && s[i+1] == '\'':
			b.WriteByte('\'')
			i++
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case '0':
				b.WriteByte(0)
			case 'b':
				b.WriteByte('\b')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'Z':
				b.WriteByte(0x1a)
			case '%', '_':
				b.WriteByte('\\')
				b.WriteByte(s[i])
			default:
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
