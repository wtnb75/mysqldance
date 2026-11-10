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

// Postgres writes SQL files for PostgreSQL. A MySQL database becomes a schema
// of the same name; every file sets what it needs itself (the search path, the
// encoding, string escaping, the time zone), because the official image runs
// each file in a session of its own:
//
//	<db>.00-schema.sql       CREATE SCHEMA, tables and indexes
//	<db>.10-data.<t>.sql     the rows of a table, in one transaction
//	<db>.90-constraints.sql  foreign keys, once all rows are in
//
// Foreign keys are applied last because the data files run in table-name
// order, which no foreign key respects. The table definitions are built from
// the catalog structure (TableInfo.Def and Schema.Defs), not from MySQL's SHOW
// CREATE text.
//
// What PostgreSQL cannot represent is reported through Schema.Warn, never
// dropped silently: generated columns, ON UPDATE CURRENT_TIMESTAMP, default
// expressions, FULLTEXT and SPATIAL indexes, expression indexes, CHECK
// constraints, foreign keys into another database or onto columns that are not
// a primary or unique key, and views. A value or a name PostgreSQL cannot hold
// is an error. AUTO_INCREMENT, ENUM/SET member lists and collations are not
// carried over, by design and without a warning (README; review ledger R-058).
type Postgres struct {
	data datafile.Options
}

// NewPostgres returns a PostgreSQL writer. opt controls the INSERT statements
// and the splitting of large tables.
func NewPostgres(opt datafile.Options) *Postgres { return &Postgres{data: opt} }

// NeedsDefs implements Writer.
func (p *Postgres) NeedsDefs() bool { return true }

// pgMaxIdent is PostgreSQL's identifier limit in bytes. A longer name is cut
// silently by the server, which could make two names one, so it is refused.
const pgMaxIdent = 63

const pgSettings = "SET client_encoding = 'UTF8';\n" +
	"SET standard_conforming_strings = on;\n" +
	"SET TimeZone = 'UTC';\n"

func pgSearchPath(db string) string {
	return "SET search_path = " + sqlvalue.QuotePGIdent(db) + ";\n"
}

// pgIdent checks that name is usable as a PostgreSQL identifier.
func pgIdent(what, name string) error {
	if len(name) > pgMaxIdent {
		return fmt.Errorf("%s %s is %d bytes, over PostgreSQL's %d-byte limit for names",
			what, sqlvalue.QuoteIdent(name), len(name), pgMaxIdent)
	}
	return nil
}

// WriteSchema implements Writer. Tables come in the order of s.Tables, each
// followed by its indexes. The primary key and the unique and ordinary indexes
// are named table__index, which is unique in the schema (PostgreSQL keeps
// indexes and tables in one namespace) or an error.
func (p *Postgres) WriteSchema(w io.Writer, s Schema) error {
	if err := pgIdent("database", s.DB); err != nil {
		return err
	}
	if strings.HasPrefix(s.DB, "pg_") {
		return fmt.Errorf("database %s cannot become a schema: PostgreSQL reserves names starting with pg_", sqlvalue.QuoteIdent(s.DB))
	}
	var out strings.Builder
	out.WriteString(pgSettings)
	out.WriteString("CREATE SCHEMA IF NOT EXISTS " + sqlvalue.QuotePGIdent(s.DB) + ";\n")
	out.WriteString(pgSearchPath(s.DB))
	// Reported only once the schema is written, so a failed run says nothing
	// about a file that was not produced.
	var warnings []string
	warn := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }

	taken := make(map[string]string, len(s.Tables))
	for _, t := range s.Tables {
		if err := pgIdent("table", t.Name); err != nil {
			return err
		}
		taken[t.Name] = "table " + sqlvalue.QuoteIdent(t.Name)
	}
	for _, t := range s.Tables {
		def, ok := s.Defs[t.Name]
		if !ok {
			return fmt.Errorf("table %s: no table definition was read", sqlvalue.QuoteIdent(t.Name))
		}
		if err := writePostgresTable(&out, t, def, taken, warn); err != nil {
			return fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
		}
	}
	for _, v := range s.Views {
		warn("view %s is not output (views are not converted to PostgreSQL)", sqlvalue.QuoteIdent(v.Name))
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return err
	}
	emit(s.Warn, warnings)
	return nil
}

func writePostgresTable(out *strings.Builder, t source.TableInfo, def source.TableDef, taken map[string]string,
	warnf func(format string, a ...any)) error {
	table := sqlvalue.QuotePGIdent(t.Name)
	myTable := sqlvalue.QuoteIdent(t.Name)
	// warn prefixes the table; the name is an argument, never part of the format.
	warn := func(format string, a ...any) { warnf("table %s: "+format, append([]any{myTable}, a...)...) }
	cols, err := outputColumns(t, def, warn)
	if err != nil {
		return err
	}
	written := make(map[string]bool, len(cols))
	var lines []string
	for _, c := range cols {
		if err := pgIdent("column", c.Name); err != nil {
			return err
		}
		line, err := postgresColumn(c, warn)
		if err != nil {
			return fmt.Errorf("column %s: %w", sqlvalue.QuoteIdent(c.Name), err)
		}
		lines = append(lines, line)
		written[c.Name] = true
	}

	var indexes []string
	for _, ix := range def.Indexes {
		myName := sqlvalue.QuoteIdent(ix.Name)
		isPrimary := ix.Name == "PRIMARY"
		switch {
		case isPrimary:
		case ix.Kind == "FULLTEXT" || ix.Kind == "SPATIAL":
			warn("index %s (%s) is not output", myName, ix.Kind)
			continue
		case slices.ContainsFunc(ix.Parts, func(p source.IndexPart) bool { return p.Column == "" }):
			warn("index %s has an expression key part and is not output", myName)
			continue
		}
		if isPrimary && slices.ContainsFunc(ix.Parts, func(p source.IndexPart) bool { return p.Desc }) {
			// PostgreSQL has no order in PRIMARY KEY, and the order means nothing
			// to a restore.
			warn("the primary key is descending; PostgreSQL's is not ordered")
			ix.Parts = ascending(ix.Parts)
		}
		cl, ok := postgresIndexColumns(ix, written)
		if !ok {
			if isPrimary {
				return fmt.Errorf("the primary key uses a column that is not output")
			}
			warn("index %s uses a generated column and is not output", myName)
			continue
		}
		if slices.ContainsFunc(ix.Parts, func(p source.IndexPart) bool { return p.Prefix }) {
			warn("index %s covers a column prefix; the whole column is indexed", myName)
		}
		full := t.Name + "__" + ix.Name
		if err := pgIdent("index", full); err != nil {
			return err
		}
		if prev, dup := taken[full]; dup {
			return fmt.Errorf("the PostgreSQL name %s of index %s is already used by %s", sqlvalue.QuoteIdent(full), myName, prev)
		}
		taken[full] = "index " + myName + " of table " + myTable
		switch {
		case isPrimary:
			lines = append(lines, "CONSTRAINT "+sqlvalue.QuotePGIdent(full)+" PRIMARY KEY ("+cl+")")
		case ix.Unique:
			indexes = append(indexes, "CREATE UNIQUE INDEX "+sqlvalue.QuotePGIdent(full)+" ON "+table+" ("+cl+");\n")
		default:
			indexes = append(indexes, "CREATE INDEX "+sqlvalue.QuotePGIdent(full)+" ON "+table+" ("+cl+");\n")
		}
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

// postgresIndexColumns renders the key parts of ix, or false if one of them is
// not a column that is output.
func postgresIndexColumns(ix source.IndexDef, written map[string]bool) (string, bool) {
	parts := make([]string, len(ix.Parts))
	for i, p := range ix.Parts {
		if !written[p.Column] {
			return "", false
		}
		parts[i] = sqlvalue.QuotePGIdent(p.Column)
		if p.Desc {
			parts[i] += " DESC"
		}
	}
	return strings.Join(parts, ", "), true
}

// ascending returns parts with the descending order cleared.
func ascending(parts []source.IndexPart) []source.IndexPart {
	out := slices.Clone(parts)
	for i := range out {
		out[i].Desc = false
	}
	return out
}

// postgresColumn renders one column definition: PostgreSQL type, NOT NULL and
// the default that can be carried over.
func postgresColumn(c source.ColumnDef, warn func(format string, a ...any)) (string, error) {
	typ, col, err := pgColumnType(c)
	if err != nil {
		return "", err
	}
	line := sqlvalue.QuotePGIdent(c.Name) + " " + typ
	if !c.Nullable {
		line += " NOT NULL"
	}
	if c.OnUpdate {
		warn("ON UPDATE of column %s is not output", sqlvalue.QuoteIdent(c.Name))
	}
	if c.Default != nil {
		if d, ok := postgresDefault(c, col); ok {
			line += " DEFAULT " + d
		} else {
			warn("default of column %s is not output (a default expression, or a value PostgreSQL cannot hold)", sqlvalue.QuoteIdent(c.Name))
		}
	}
	return line, nil
}

var (
	parenNumber = regexp.MustCompile(`\((\d+)\)`)
	parenPair   = regexp.MustCompile(`\((\d+),\s*(\d+)\)`)
)

// pgPrecision returns the n of "datetime(n)", or 0.
func pgPrecision(columnType string) string {
	if m := parenNumber.FindStringSubmatch(columnType); m != nil {
		return m[1]
	}
	return "0"
}

// pgColumnType maps a MySQL column to the PostgreSQL type that keeps its
// values exact, and says how its values are written. The rules:
//
//   - TINYINT(1) is boolean; every other integer is the narrowest type that
//     holds the whole range, including the unsigned one (BIGINT UNSIGNED is
//     numeric(20,0)).
//   - CHAR is varchar, because PostgreSQL char(n) pads and MySQL's does not.
//   - TIME is interval, because its range (-838:59:59 to 838:59:59) is wider
//     than the time of day.
//   - TIMESTAMP is timestamptz and DATETIME is timestamp, both at the
//     precision of the MySQL column. The dump is in UTC and so is every file.
func pgColumnType(c source.ColumnDef) (string, sqlvalue.PGColumn, error) {
	unsigned := isUnsigned(c)
	num := sqlvalue.PGColumn{Kind: sqlvalue.PGNumber}
	switch c.DataType {
	case "tinyint":
		// An unsigned TINYINT is never a boolean, whether the server reports
		// its display width ("tinyint(1) unsigned") or not ("tinyint unsigned").
		if strings.HasPrefix(c.Type, "tinyint(1)") && !unsigned {
			return "boolean", sqlvalue.PGColumn{Kind: sqlvalue.PGBool}, nil
		}
		return "smallint", num, nil
	case "smallint":
		if unsigned {
			return "integer", num, nil
		}
		return "smallint", num, nil
	case "year":
		return "smallint", num, nil
	case "mediumint":
		return "integer", num, nil
	case "int", "integer":
		if unsigned {
			return "bigint", num, nil
		}
		return "integer", num, nil
	case "bigint":
		if unsigned {
			return "numeric(20,0)", num, nil
		}
		return "bigint", num, nil
	case "bit":
		width := 1
		if m := bitWidth.FindStringSubmatch(c.Type); m != nil {
			width, _ = strconv.Atoi(m[1])
		}
		return "bit(" + strconv.Itoa(width) + ")", sqlvalue.PGColumn{Kind: sqlvalue.PGBit, Bits: width}, nil
	case "decimal", "numeric":
		if m := parenPair.FindStringSubmatch(c.Type); m != nil {
			return "numeric(" + m[1] + "," + m[2] + ")", num, nil
		}
		if m := parenNumber.FindStringSubmatch(c.Type); m != nil {
			return "numeric(" + m[1] + ",0)", num, nil
		}
		return "numeric(10,0)", num, nil
	case "float":
		return "real", num, nil
	case "double", "real":
		return "double precision", num, nil
	case "char", "varchar":
		text := sqlvalue.PGColumn{Kind: sqlvalue.PGText}
		if m := parenNumber.FindStringSubmatch(c.Type); m != nil && m[1] != "0" {
			return "varchar(" + m[1] + ")", text, nil
		}
		return "text", text, nil
	case "tinytext", "text", "mediumtext", "longtext", "enum", "set":
		return "text", sqlvalue.PGColumn{Kind: sqlvalue.PGText}, nil
	case "json":
		return "json", sqlvalue.PGColumn{Kind: sqlvalue.PGText}, nil
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob",
		"geometry", "point", "linestring", "polygon", "multipoint", "multilinestring",
		"multipolygon", "geometrycollection", "geomcollection":
		return "bytea", sqlvalue.PGColumn{Kind: sqlvalue.PGBytea}, nil
	case "date":
		return "date", sqlvalue.PGColumn{Kind: sqlvalue.PGDate}, nil
	case "datetime":
		return "timestamp(" + pgPrecision(c.Type) + ")", sqlvalue.PGColumn{Kind: sqlvalue.PGTimestamp}, nil
	case "timestamp":
		return "timestamptz(" + pgPrecision(c.Type) + ")", sqlvalue.PGColumn{Kind: sqlvalue.PGTimestamp}, nil
	case "time":
		return "interval(" + pgPrecision(c.Type) + ")", sqlvalue.PGColumn{Kind: sqlvalue.PGTime}, nil
	}
	return "", sqlvalue.PGColumn{}, fmt.Errorf("column type %q has no PostgreSQL counterpart", c.Type)
}

// postgresDefault returns the DEFAULT of c as PostgreSQL text, or false when
// the default cannot be carried over.
func postgresDefault(c source.ColumnDef, col sqlvalue.PGColumn) (string, bool) {
	d := *c.Default
	if c.DefaultExpr {
		if currentTimestamp.MatchString(strings.ToUpper(strings.TrimSpace(d))) {
			// The precision n of CURRENT_TIMESTAMP(n) is the column's, which the
			// column type already carries; PostgreSQL rounds a default to it (R-059).
			return "CURRENT_TIMESTAMP", true
		}
		return "", false
	}
	raw := []byte(d)
	if c.DataType == "bit" {
		// The catalog shows a BIT default as b'101'.
		bits := strings.TrimSuffix(strings.TrimPrefix(d, "b'"), "'")
		n, ok := new(big.Int).SetString(bits, 2)
		if !ok {
			return "", false
		}
		raw = n.Bytes()
	}
	lit, err := sqlvalue.PostgresLiteral(col, raw, false)
	if err != nil {
		return "", false
	}
	return lit, true
}

// DataParts implements Writer. The values are rendered as PostgreSQL literals
// first; the split and the INSERT boundaries are decided on the rendered text.
// Each file runs in one transaction. All rows are held in memory, like the
// MySQL writer. A value PostgreSQL cannot hold makes the only returned part
// fail with the table and column, so the dump stops instead of changing it.
func (p *Postgres) DataParts(db string, t source.TableInfo, rows [][]sqlvalue.Value) []Part {
	fail := func(err error) []Part {
		err = fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
		return []Part{func(io.Writer) error { return err }}
	}
	if t.Def == nil {
		return fail(fmt.Errorf("no table definition was read"))
	}
	defs, err := outputColumns(t, *t.Def, nil)
	if err != nil {
		return fail(err)
	}
	kinds := make([]sqlvalue.PGColumn, len(defs))
	for i, c := range defs {
		if _, kinds[i], err = pgColumnType(c); err != nil {
			return fail(fmt.Errorf("column %s: %w", sqlvalue.QuoteIdent(c.Name), err))
		}
	}

	var rowErr error
	literal := func(row []sqlvalue.Value) []string {
		if rowErr != nil {
			return nil
		}
		if len(row) != len(kinds) {
			rowErr = fmt.Errorf("a row has %d values for %d columns", len(row), len(kinds))
			return nil
		}
		out := make([]string, len(row))
		for i, v := range row {
			lit, err := sqlvalue.PostgresLiteral(kinds[i], v.Bytes(), v.IsNull())
			if err != nil {
				rowErr = fmt.Errorf("column %s: %w", sqlvalue.QuoteIdent(defs[i].Name), err)
				return nil
			}
			out[i] = lit
		}
		return out
	}
	header := pgSettings + pgSearchPath(db) + "BEGIN;\n"
	parts := splitParts(t, rows, p.data.RowsPerFile, literal,
		func(w io.Writer, cols []string, group [][]string) error {
			return datafile.WriteStatements(w, header, "COMMIT;\n", sqlvalue.QuotePGIdent, t.Name, cols, t.ListColumns, group, p.data)
		})
	if rowErr != nil {
		return fail(rowErr)
	}
	return parts
}

// WriteConstraints implements ConstraintWriter: the foreign keys of every
// table, applied after all rows are loaded. PostgreSQL wants the referenced
// columns to be a primary key or a unique index, where InnoDB takes any index;
// a key that is not is reported and left out.
func (p *Postgres) WriteConstraints(w io.Writer, s Schema) error {
	var out strings.Builder
	out.WriteString(pgSettings)
	out.WriteString(pgSearchPath(s.DB))
	var warnings []string
	byName := make(map[string]source.TableInfo, len(s.Tables))
	for _, t := range s.Tables {
		byName[t.Name] = t
	}
	outputs := func(t source.TableInfo) map[string]bool {
		set := map[string]bool{}
		for _, c := range s.Defs[t.Name].Columns {
			if !c.Generated {
				set[c.Name] = true
			}
		}
		return set
	}

	for _, t := range s.Tables {
		def, ok := s.Defs[t.Name]
		if !ok {
			return fmt.Errorf("table %s: no table definition was read", sqlvalue.QuoteIdent(t.Name))
		}
		have := outputs(t)
		for _, fk := range def.ForeignKeys {
			skip := func(why string) {
				warnings = append(warnings, fmt.Sprintf("table %s: foreign key %s %s and is not output",
					sqlvalue.QuoteIdent(t.Name), sqlvalue.QuoteIdent(fk.Name), why))
			}
			if err := pgIdent("foreign key", fk.Name); err != nil {
				return err
			}
			ref, inDB := byName[fk.RefTable]
			refHave := outputs(ref)
			switch {
			case fk.RefSchema != s.DB:
				skip("references another database")
				continue
			case !inDB:
				skip("references table " + sqlvalue.QuoteIdent(fk.RefTable) + ", which is not output")
				continue
			case slices.ContainsFunc(fk.Columns, func(c string) bool { return !have[c] }):
				skip("uses a generated column")
				continue
			case slices.ContainsFunc(fk.RefColumns, func(c string) bool { return !refHave[c] }):
				skip("references a generated column")
				continue
			case !isUniqueKey(s.Defs[ref.Name], fk.RefColumns):
				skip("references columns that are not a primary key or unique key")
				continue
			}
			out.WriteString("ALTER TABLE " + sqlvalue.QuotePGIdent(t.Name) + " ADD CONSTRAINT " + sqlvalue.QuotePGIdent(fk.Name) +
				" FOREIGN KEY (" + pgList(fk.Columns) + ") REFERENCES " + sqlvalue.QuotePGIdent(fk.RefTable) +
				" (" + pgList(fk.RefColumns) + ") ON DELETE " + fk.OnDelete + " ON UPDATE " + fk.OnUpdate + ";\n")
		}
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return err
	}
	emit(s.Warn, warnings)
	return nil
}

// isUniqueKey reports whether cols are exactly the columns of the primary key
// or of a unique index of def, in any order. A prefix key part does not count.
func isUniqueKey(def source.TableDef, cols []string) bool {
	want := slices.Sorted(slices.Values(cols))
	for _, ix := range def.Indexes {
		if !ix.Unique || ix.Kind == "FULLTEXT" || ix.Kind == "SPATIAL" {
			continue
		}
		if slices.ContainsFunc(ix.Parts, func(p source.IndexPart) bool { return p.Column == "" || p.Prefix }) {
			continue
		}
		names := make([]string, len(ix.Parts))
		for i, p := range ix.Parts {
			names[i] = p.Column
		}
		if slices.Equal(slices.Sorted(slices.Values(names)), want) {
			return true
		}
	}
	return false
}

func pgList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = sqlvalue.QuotePGIdent(n)
	}
	return strings.Join(q, ", ")
}
