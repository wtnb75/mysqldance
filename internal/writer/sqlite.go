package writer

import (
	"fmt"
	"io"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"mysqldance/internal/datafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// SQLite writes SQL files for SQLite. The files of one MySQL database restore
// into one SQLite database, e.g. `cat out/mydb.*.sql | sqlite3 mydb.db`: there
// is no CREATE DATABASE or USE, and the table definitions are built from the
// catalog structure (Schema.Defs), not from MySQL's SHOW CREATE text.
//
// What SQLite cannot represent is reported through Schema.Warn, never dropped
// silently: generated columns, ON UPDATE CURRENT_TIMESTAMP, default
// expressions, FULLTEXT and SPATIAL indexes, expression indexes, CHECK
// constraints, foreign keys into another database, and views.
type SQLite struct {
	data datafile.Options
}

// NewSQLite returns a SQLite writer. opt controls the INSERT statements and the
// splitting of large tables.
func NewSQLite(opt datafile.Options) *SQLite { return &SQLite{data: opt} }

// NeedsDefs implements Writer.
func (s *SQLite) NeedsDefs() bool { return true }

const (
	sqlitePragma     = "PRAGMA foreign_keys=OFF;\n"
	sqliteDataHeader = sqlitePragma + "BEGIN;\n"
	sqliteDataFooter = "COMMIT;\n"
)

// WriteSchema implements Writer. Tables come in the order of s.Tables, each
// followed by its indexes; foreign keys are part of the table, which SQLite
// accepts before the referenced table exists.
func (s *SQLite) WriteSchema(w io.Writer, sc Schema) error {
	var out strings.Builder
	out.WriteString(sqlitePragma)
	// Reported only once the schema is written, so a failed run says nothing
	// about a file that was not produced.
	var warnings []string
	warn := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }

	// SQLite keeps tables and indexes in one namespace, ignoring ASCII case.
	// Table names are unique in lower case here: dump rejects file names that
	// collide in case (R-053).
	taken := make(map[string]string, len(sc.Tables))
	for _, t := range sc.Tables {
		taken[strings.ToLower(t.Name)] = "table " + sqlvalue.QuoteIdent(t.Name)
	}
	for _, t := range sc.Tables {
		def, ok := sc.Defs[t.Name]
		if !ok {
			return fmt.Errorf("table %s: no table definition was read", sqlvalue.QuoteIdent(t.Name))
		}
		if err := writeSQLiteTable(&out, sc.DB, t, def, sc.Defs, taken, warn); err != nil {
			return fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
		}
	}
	writeSQLiteViews(&out, sc, warn)
	if _, err := io.WriteString(w, out.String()); err != nil {
		return err
	}
	emit(sc.Warn, warnings)
	return nil
}

func writeSQLiteTable(out *strings.Builder, db string, t source.TableInfo, def source.TableDef, defs map[string]source.TableDef,
	taken map[string]string, warnf func(format string, a ...any)) error {
	table := sqlvalue.QuoteIdent(t.Name)
	// warn prefixes the table; the name is an argument, never part of the format.
	warn := func(format string, a ...any) { warnf("table %s: "+format, append([]any{table}, a...)...) }
	// The columns written must be the ones the data files list.
	cols, err := outputColumns(t, def, warn)
	if err != nil {
		return err
	}
	written := make(map[string]bool, len(cols))
	for _, c := range cols {
		written[c.Name] = true
	}

	var lines []string
	for _, c := range cols {
		line, err := sqliteColumn(c, warn)
		if err != nil {
			return fmt.Errorf("column %s: %w", sqlvalue.QuoteIdent(c.Name), err)
		}
		lines = append(lines, line)
	}

	var indexes []string
	for _, ix := range def.Indexes {
		name := sqlvalue.QuoteIdent(ix.Name)
		if ix.Name == "PRIMARY" {
			cl, ok := indexColumns(ix, written)
			if !ok {
				return fmt.Errorf("the primary key uses a column that is not output")
			}
			if slices.ContainsFunc(ix.Parts, func(p source.IndexPart) bool { return p.Prefix }) {
				warn("index %s covers a column prefix; the whole column is indexed", name)
			}
			lines = append(lines, "PRIMARY KEY ("+cl+")")
			continue
		}
		switch {
		case ix.Kind == "FULLTEXT" || ix.Kind == "SPATIAL":
			warn("index %s (%s) is not output", name, ix.Kind)
			continue
		case slices.ContainsFunc(ix.Parts, func(p source.IndexPart) bool { return p.Column == "" }):
			warn("index %s has an expression key part and is not output", name)
			continue
		}
		cl, ok := indexColumns(ix, written)
		if !ok {
			warn("index %s uses a generated column and is not output", name)
			continue
		}
		if slices.ContainsFunc(ix.Parts, func(p source.IndexPart) bool { return p.Prefix }) {
			warn("index %s covers a column prefix; the whole column is indexed", name)
		}
		full := t.Name + "__" + ix.Name
		if prev, dup := taken[strings.ToLower(full)]; dup {
			return fmt.Errorf("the SQLite name %s of index %s is already used by %s", sqlvalue.QuoteIdent(full), name, prev)
		}
		taken[strings.ToLower(full)] = "index " + name + " of table " + table
		create := "CREATE INDEX "
		if ix.Unique {
			create = "CREATE UNIQUE INDEX "
		}
		indexes = append(indexes, create+sqlvalue.QuoteIdent(full)+" ON "+table+" ("+cl+");\n")
	}

	for _, fk := range def.ForeignKeys {
		name := sqlvalue.QuoteIdent(fk.Name)
		if fk.RefSchema != db {
			warn("foreign key %s references another database and is not output", name)
			continue
		}
		if slices.ContainsFunc(fk.Columns, func(c string) bool { return !written[c] }) {
			warn("foreign key %s uses a generated column and is not output", name)
			continue
		}
		// InnoDB accepts any index on the referenced columns; SQLite needs the
		// primary key or a unique index there, or writing to the child fails with
		// "foreign key mismatch" once foreign keys are on. A parent that was not
		// read is not judged: the reference is carried over as it was.
		if parent, ok := defs[fk.RefTable]; ok && !isSQLiteParentKey(parent, fk.RefColumns) {
			warn("foreign key %s is not output: its columns in %s are not the primary key or a unique index",
				name, sqlvalue.QuoteIdent(fk.RefTable))
			continue
		}
		lines = append(lines, "FOREIGN KEY ("+quoteList(fk.Columns)+") REFERENCES "+sqlvalue.QuoteIdent(fk.RefTable)+
			" ("+quoteList(fk.RefColumns)+") ON DELETE "+fk.OnDelete+" ON UPDATE "+fk.OnUpdate)
	}
	for _, c := range def.Checks {
		warn("CHECK constraint %s is not output", sqlvalue.QuoteIdent(c))
	}

	out.WriteString("CREATE TABLE " + table + " (\n  " + strings.Join(lines, ",\n  ") + "\n);\n")
	for _, ix := range indexes {
		out.WriteString(ix)
	}
	return nil
}

// sqliteColumn renders one column definition: SQLite type, NOT NULL and the
// default that can be carried over.
func sqliteColumn(c source.ColumnDef, warn func(format string, a ...any)) (string, error) {
	typ, err := sqliteType(c)
	if err != nil {
		return "", err
	}
	line := sqlvalue.QuoteIdent(c.Name) + " " + typ
	if !c.Nullable {
		line += " NOT NULL"
	}
	if c.OnUpdate {
		warn("ON UPDATE of column %s is not output", sqlvalue.QuoteIdent(c.Name))
	}
	if c.Default != nil {
		if d, ok := sqliteDefault(c); ok {
			line += " DEFAULT " + d
		} else {
			warn("default expression of column %s is not output", sqlvalue.QuoteIdent(c.Name))
		}
	}
	return line, nil
}

// isUnsigned reports whether c is an UNSIGNED number. It looks at the end of
// COLUMN_TYPE ("bigint unsigned", "int unsigned zerofill"), not anywhere in it,
// because an ENUM member may be called "unsigned".
func isUnsigned(c source.ColumnDef) bool {
	t := strings.TrimSuffix(c.Type, " zerofill")
	return strings.HasSuffix(t, " unsigned")
}

var bitWidth = regexp.MustCompile(`^bit\((\d+)\)`)

// sqliteType maps a MySQL column to the SQLite type that keeps its values
// exact: integers that fit in 64 bits stay INTEGER; DECIMAL, BIGINT UNSIGNED
// and BIT(64) are TEXT because SQLite would turn them into a lossy REAL.
func sqliteType(c source.ColumnDef) (string, error) {
	switch c.DataType {
	case "tinyint", "smallint", "mediumint", "int", "integer", "year":
		return "INTEGER", nil
	case "bigint":
		if isUnsigned(c) {
			return "TEXT", nil
		}
		return "INTEGER", nil
	case "bit":
		width := 1
		if m := bitWidth.FindStringSubmatch(c.Type); m != nil {
			width, _ = strconv.Atoi(m[1])
		}
		if width > 63 {
			return "TEXT", nil
		}
		return "INTEGER", nil
	case "decimal", "numeric":
		return "TEXT", nil
	case "float", "double", "real":
		return "REAL", nil
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob",
		"geometry", "point", "linestring", "polygon", "multipoint", "multilinestring",
		"multipolygon", "geometrycollection", "geomcollection":
		return "BLOB", nil
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set", "json",
		"date", "time", "datetime", "timestamp":
		return "TEXT", nil
	}
	return "", fmt.Errorf("column type %q has no SQLite counterpart", c.Type)
}

var currentTimestamp = regexp.MustCompile(`^CURRENT_TIMESTAMP(\(\d*\))?$`)

// sqliteDefault returns the DEFAULT of c as SQLite text, or false when the
// default cannot be carried over.
func sqliteDefault(c source.ColumnDef) (string, bool) {
	d := *c.Default
	if c.DefaultExpr {
		if currentTimestamp.MatchString(strings.ToUpper(strings.TrimSpace(d))) {
			return "CURRENT_TIMESTAMP", true
		}
		return "", false
	}
	typeName := strings.ToUpper(c.DataType)
	raw := []byte(d)
	if c.DataType == "bit" {
		// The catalog shows a BIT default as b'101'.
		bits := strings.TrimSuffix(strings.TrimPrefix(d, "b'"), "'")
		n, ok := new(big.Int).SetString(bits, 2)
		if !ok {
			return "", false
		}
		raw = n.Bytes()
	} else if isUnsigned(c) {
		typeName = "UNSIGNED " + typeName
	}
	lit := sqlvalue.SQLiteLiteral(typeName, raw, false)
	if strings.HasPrefix(lit, "CAST(") {
		// A DEFAULT takes a literal or a parenthesized expression.
		lit = "(" + lit + ")"
	}
	return lit, true
}

// indexColumns renders the key parts of ix, or false if one of them is not a
// column that is output.
func indexColumns(ix source.IndexDef, written map[string]bool) (string, bool) {
	parts := make([]string, len(ix.Parts))
	for i, p := range ix.Parts {
		if !written[p.Column] {
			return "", false
		}
		parts[i] = sqlvalue.QuoteIdent(p.Column)
		if p.Desc {
			parts[i] += " DESC"
		}
	}
	return strings.Join(parts, ", "), true
}

// isSQLiteParentKey reports whether cols, in any order, are exactly the columns
// of the primary key or of a unique index of parent that the SQLite output
// carries (not FULLTEXT / SPATIAL, not an expression, not a generated column).
// Prefixes, DESC and collations need no check: InnoDB cannot reference a prefix
// index, SQLite ignores the sort order here, and no column or index in this
// output names a collation.
func isSQLiteParentKey(parent source.TableDef, cols []string) bool {
	generated := map[string]bool{}
	for _, c := range parent.Columns {
		if c.Generated {
			generated[c.Name] = true
		}
	}
	want := slices.Sorted(slices.Values(cols))
	return slices.ContainsFunc(parent.Indexes, func(ix source.IndexDef) bool {
		if !ix.Unique || ix.Kind == "FULLTEXT" || ix.Kind == "SPATIAL" {
			return false
		}
		names := make([]string, len(ix.Parts))
		for i, p := range ix.Parts {
			if p.Column == "" || generated[p.Column] {
				return false
			}
			names[i] = p.Column
		}
		slices.Sort(names)
		return slices.Equal(names, want)
	})
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = sqlvalue.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}

// NewRowWriter implements Writer. Each value is rendered as a SQLite literal
// when its row arrives; the split and the INSERT boundaries are decided on the
// rendered text of that row alone. Each file runs in one transaction, so
// restoring a large table is not one commit per statement.
func (s *SQLite) NewRowWriter(_ string, t source.TableInfo, open OpenFile) (RowWriter, error) {
	return newStreamWriter(t, open, sqliteDataHeader, sqliteDataFooter, sqlvalue.QuoteIdent, s.data,
		infallible(sqlvalue.SQLiteLiterals)), nil
}
