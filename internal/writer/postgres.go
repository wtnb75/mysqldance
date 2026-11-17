package writer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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
// constraints, foreign keys into a database that is not dumped or onto columns
// that are not a primary or unique key, and the views that cannot be converted
// (postgresview.go). A value or a name PostgreSQL cannot hold
// is an error. AUTO_INCREMENT, ENUM/SET member lists and collations are not
// carried over, by design and without a warning (README; review ledger R-058).
type Postgres struct {
	data datafile.Options
	// schemas maps a MySQL database name to the schema it becomes, for the
	// databases named by --pg-schema; any other database keeps its own name.
	schemas map[string]string
}

// NewPostgres returns a PostgreSQL writer. opt controls the INSERT statements
// and the splitting of large tables.
func NewPostgres(opt datafile.Options) *Postgres { return &Postgres{data: opt} }

// WithSchemaNames sets the schema names given by --pg-schema (see
// ParsePGSchemas) and returns p.
func (p *Postgres) WithSchemaNames(m map[string]string) *Postgres {
	p.schemas = m
	return p
}

// schemaOf returns the name of the PostgreSQL schema the database db becomes.
func (p *Postgres) schemaOf(db string) string {
	if s, ok := p.schemas[db]; ok {
		return s
	}
	return db
}

// ParsePGSchemas parses the values of --pg-schema (DB=SCHEMA) for the databases
// dbs that are dumped. A DB must be dumped and given once, a SCHEMA must be a
// name PostgreSQL can hold and not start with pg_, and no two databases may end
// up with the same schema name. It returns nil when there are no specs.
func ParsePGSchemas(specs, dbs []string) (map[string]string, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(specs))
	for _, spec := range specs {
		db, schema, ok := strings.Cut(spec, "=")
		_, given := m[db]
		switch {
		case !ok || db == "" || schema == "":
			return nil, fmt.Errorf("%q is not DB=SCHEMA", spec)
		case !slices.Contains(dbs, db):
			return nil, fmt.Errorf("database %s is not one of the databases to dump", sqlvalue.QuoteIdent(db))
		case given:
			return nil, fmt.Errorf("database %s is given more than once", sqlvalue.QuoteIdent(db))
		case strings.HasPrefix(schema, "pg_") || schema == "information_schema":
			return nil, fmt.Errorf("schema %s cannot be used: PostgreSQL reserves it (names starting with pg_, and information_schema)", sqlvalue.QuoteIdent(schema))
		}
		if err := pgIdent("schema", schema); err != nil {
			return nil, err
		}
		m[db] = schema
	}
	// Each database ends up under one schema name, so two must not share one.
	owner := make(map[string]string, len(dbs))
	for _, db := range dbs {
		name := db
		if s, ok := m[db]; ok {
			name = s
		}
		// A database named twice is not a clash of this flag.
		if prev, dup := owner[name]; dup && prev != db {
			return nil, fmt.Errorf("databases %s and %s would both become schema %s",
				sqlvalue.QuoteIdent(prev), sqlvalue.QuoteIdent(db), sqlvalue.QuoteIdent(name))
		}
		owner[name] = db
	}
	return m, nil
}

// NeedsDefs implements Writer.
func (p *Postgres) NeedsDefs() bool { return true }

// pgMaxIdent is PostgreSQL's identifier limit in bytes. A longer name is cut
// silently by the server, which could make two names one, so it is refused
// (pgIdent: database names) or, for a table, column, index or foreign key
// name, shortened by us (pgShortName).
const pgMaxIdent = 63

const pgSettings = "SET client_encoding = 'UTF8';\n" +
	"SET standard_conforming_strings = on;\n" +
	"SET TimeZone = 'UTC';\n"

func pgSearchPath(db string) string {
	return "SET search_path = " + sqlvalue.QuotePGIdent(db) + ";\n"
}

// pgHashBytes is the number of bytes of the hash (two hex digits each) that
// ends a shortened name.
const pgHashBytes = 4

// pgShortName returns name unchanged if PostgreSQL can hold it. Otherwise it
// returns the start of name, cut on a character boundary, followed by "_" and
// the first pgHashBytes bytes of the SHA-256 of the whole name in hex, in
// pgMaxIdent bytes. The result depends on name alone, so a run gives the same
// bytes every time, and names that differ only after the cut still differ.
func pgShortName(name string) string {
	if len(name) <= pgMaxIdent {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:pgHashBytes])
	cut := pgMaxIdent - len(suffix)
	// Back off to the start of a character, so the prefix is whole characters.
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut] + suffix
}

// pgQuote quotes name as WriteSchema created it: shortened by pgShortName if
// over the limit. A name pgShortName gave is at the limit, so it is not changed
// again.
func pgQuote(name string) string { return sqlvalue.QuotePGIdent(pgShortName(name)) }

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
// are named table__index (shortened by pgShortName if over the limit), which is
// unique in the schema (PostgreSQL keeps indexes and tables in one namespace)
// or an error. A table name over the limit is shortened too; taken is keyed by
// these PostgreSQL names, while the index names are built from the MySQL name
// of the table.
func (p *Postgres) WriteSchema(w io.Writer, s Schema) error {
	schema := p.schemaOf(s.DB)
	// The limit applies to the schema name, so a database over it can be renamed.
	if err := pgIdent("database", schema); err != nil {
		return err
	}
	if strings.HasPrefix(schema, "pg_") {
		return fmt.Errorf("database %s cannot become a schema: PostgreSQL reserves names starting with pg_ (name another schema with --pg-schema %s=SCHEMA)",
			sqlvalue.QuoteIdent(s.DB), s.DB)
	}
	var out strings.Builder
	out.WriteString(pgSettings)
	out.WriteString("CREATE SCHEMA IF NOT EXISTS " + sqlvalue.QuotePGIdent(schema) + ";\n")
	out.WriteString(pgSearchPath(schema))
	// Reported only once the schema is written, so a failed run says nothing
	// about a file that was not produced.
	var warnings warnList

	// taken holds the PostgreSQL names. A table name over the limit is shortened
	// here (and everywhere the table is named, through pgShortName); a failed run
	// prints no warnings, so an error names the table as MySQL has it.
	taken := make(map[string]string, len(s.Tables))
	for _, t := range s.Tables {
		name, myTable := pgShortName(t.Name), sqlvalue.QuoteIdent(t.Name)
		if prev, dup := taken[name]; dup {
			return fmt.Errorf("the PostgreSQL name %s of table %s is already used by %s", sqlvalue.QuoteIdent(name), myTable, prev)
		}
		if name != t.Name {
			warnings.add(t.Name, "table %s is named %s in PostgreSQL (over %d bytes)", myTable, sqlvalue.QuoteIdent(name), pgMaxIdent)
		}
		taken[name] = "table " + myTable
	}
	for _, t := range s.Tables {
		def, ok := s.Defs[t.Name]
		if !ok {
			return fmt.Errorf("table %s: no table definition was read", sqlvalue.QuoteIdent(t.Name))
		}
		if err := writePostgresTable(&out, t, def, taken, warnings.add); err != nil {
			return fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
		}
	}
	env := newPGViewEnv(s.Defs)
	writeViews(&out, s, pgDialect, &env, taken, warnings.noTable)
	if _, err := io.WriteString(w, out.String()); err != nil {
		return err
	}
	emit(s.Warn, warnings)
	return nil
}

func writePostgresTable(out *strings.Builder, t source.TableInfo, def source.TableDef, taken map[string]string,
	warnf func(table, format string, a ...any)) error {
	table := sqlvalue.QuotePGIdent(pgShortName(t.Name))
	myTable := sqlvalue.QuoteIdent(t.Name)
	// warn prefixes the table; the name is an argument, never part of the format.
	warn := func(format string, a ...any) { warnf(t.Name, "table %s: "+format, append([]any{myTable}, a...)...) }
	cols, err := outputColumns(t, def, warn)
	if err != nil {
		return err
	}
	written := make(map[string]bool, len(cols))
	// colNames holds the PostgreSQL names of the columns of this table. A failed run
	// prints no warnings, so a clash names both columns as MySQL has them.
	colNames := make(map[string]string, len(cols))
	var lines []string
	for _, c := range cols {
		myCol := sqlvalue.QuoteIdent(c.Name)
		short := pgShortName(c.Name)
		if prev, dup := colNames[short]; dup {
			return fmt.Errorf("the PostgreSQL name %s of column %s is already used by column %s", sqlvalue.QuoteIdent(short), myCol, prev)
		}
		colNames[short] = myCol
		if short != c.Name {
			warn("column %s is named %s in PostgreSQL (over %d bytes)", myCol, sqlvalue.QuoteIdent(short), pgMaxIdent)
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
		// An index name is built from two MySQL names, so MySQL's own limits do not
		// keep it within PostgreSQL's. It is built from the table's MySQL name, not
		// from the shortened one, so it does not change when the table name is
		// shortened. A column name is shortened too, but the index name does not carry it.
		full := t.Name + "__" + ix.Name
		// who describes this index in an error. A failed run prints no warnings, so the
		// error has to carry the name that was shortened.
		who := "index " + myName + " of table " + myTable
		if short := pgShortName(full); short != full {
			warn("index %s is named %s in PostgreSQL, not %s (over %d bytes)",
				myName, sqlvalue.QuoteIdent(short), sqlvalue.QuoteIdent(full), pgMaxIdent)
			who += " (shortened from " + sqlvalue.QuoteIdent(full) + ")"
			full = short
		}
		if prev, dup := taken[full]; dup {
			return fmt.Errorf("the PostgreSQL name %s of %s is already used by %s", sqlvalue.QuoteIdent(full), who, prev)
		}
		taken[full] = who
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
		parts[i] = pgQuote(p.Column)
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
	line := pgQuote(c.Name) + " " + typ
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

// isPGBoolean reports whether the column becomes a PostgreSQL boolean. An
// unsigned TINYINT is never one, whether the server reports its display width
// ("tinyint(1) unsigned") or not ("tinyint unsigned").
func isPGBoolean(c source.ColumnDef) bool {
	return c.DataType == "tinyint" && strings.HasPrefix(c.Type, "tinyint(1)") && !isUnsigned(c)
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
	flt := sqlvalue.PGColumn{Kind: sqlvalue.PGFloat}
	switch c.DataType {
	case "tinyint":
		if isPGBoolean(c) {
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
		return "real", flt, nil
	case "double", "real":
		return "double precision", flt, nil
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

// NewRowWriter implements Writer. Each value is rendered as a PostgreSQL
// literal when its row arrives; the split and the INSERT boundaries are decided
// on the rendered text of that row and the bytes since the previous cut (see
// datafile.Write). Each file runs in one transaction. A
// table or a value PostgreSQL cannot hold fails with the table (and column), so
// the dump stops instead of changing it.
func (p *Postgres) NewRowWriter(db string, t source.TableInfo, open OpenFile) (RowWriter, error) {
	wrap := func(err error) error {
		return fmt.Errorf("table %s: %w", sqlvalue.QuoteIdent(t.Name), err)
	}
	fail := func(err error) (RowWriter, error) { return nil, wrap(err) }
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

	literal := func(row []sqlvalue.Value) ([]string, error) {
		if len(row) != len(kinds) {
			return nil, wrap(fmt.Errorf("a row has %d values for %d columns", len(row), len(kinds)))
		}
		out := make([]string, len(row))
		for i, v := range row {
			lit, err := sqlvalue.PostgresLiteral(kinds[i], v.Bytes(), v.IsNull())
			if err != nil {
				return nil, wrap(fmt.Errorf("column %s: %w", sqlvalue.QuoteIdent(defs[i].Name), err))
			}
			out[i] = lit
		}
		return out, nil
	}
	header := pgSettings + pgSearchPath(p.schemaOf(db)) + "BEGIN;\n"
	// pgQuote names the table and its columns as WriteSchema created them.
	return newStreamWriter(t, open, header, "COMMIT;\n", pgQuote, p.data, literal), nil
}

// pgConstraintDB is one database of the run, with its tables by name.
type pgConstraintDB struct {
	Schema
	tables map[string]source.TableInfo
}

// outputs returns the columns of t that are output (all but the generated ones).
func (d pgConstraintDB) outputs(t source.TableInfo) map[string]bool {
	set := map[string]bool{}
	for _, c := range d.Defs[t.Name].Columns {
		if !c.Generated {
			set[c.Name] = true
		}
	}
	return set
}

// WriteConstraints implements ConstraintWriter: the foreign keys, applied after
// all rows are loaded. PostgreSQL wants the referenced columns to be a primary
// key or a unique index, where InnoDB takes any index; a key that is not is
// reported and left out.
//
// A foreign key between two databases is in the constraints file of the one
// whose file name sorts later (byte order, as the restore runs the files): that
// file runs after the schema and data files of both. So the file of s holds the
// keys of every database, s included, whose host is s, in the order of the file
// names of the databases and then of the tables, and not the order the databases
// were dumped in. Whatever is wrong with a key is reported, and a name that
// clashes is an error, by the database of the table that has the key only, so
// each is said once.
func (p *Postgres) WriteConstraints(w io.Writer, s Schema) error {
	var out strings.Builder
	out.WriteString(pgSettings)
	out.WriteString(pgSearchPath(p.schemaOf(s.DB)))
	var warnings warnList

	dbs := make([]pgConstraintDB, 0, 1+len(s.Peers))
	for _, d := range append([]Schema{s}, s.Peers...) {
		tables := make(map[string]source.TableInfo, len(d.Tables))
		for _, t := range d.Tables {
			tables[t.Name] = t
		}
		dbs = append(dbs, pgConstraintDB{Schema: d, tables: tables})
	}
	slices.SortStableFunc(dbs, func(a, b pgConstraintDB) int { return strings.Compare(a.ConstraintsFile, b.ConstraintsFile) })
	byDB := make(map[string]pgConstraintDB, len(dbs))
	for _, d := range dbs {
		byDB[d.DB] = d
	}
	// qualified names a table (as WriteSchema created it, so shortened if the name
	// is over the limit) of another database, which the search path of this file
	// does not reach.
	qualified := func(db, table string) string {
		table = sqlvalue.QuotePGIdent(pgShortName(table))
		if db == s.DB {
			return table
		}
		return sqlvalue.QuotePGIdent(p.schemaOf(db)) + "." + table
	}

	for _, d := range dbs {
		own := d.DB == s.DB
		for _, t := range d.Tables {
			def, ok := d.Defs[t.Name]
			if !ok {
				if !own {
					continue
				}
				return fmt.Errorf("table %s: no table definition was read", sqlvalue.QuoteIdent(t.Name))
			}
			have := d.outputs(t)
			// Constraint names are unique within a table, and the primary key is one.
			// Foreign key names are unique in MySQL, so a clash comes from a shortening.
			// The primary key's name is the one WriteSchema gives it (<table>__PRIMARY,
			// shortened); WriteSchema fails the run if the key is not output, so it is
			// always there. Review ledger R-126, R-127.
			used := map[string]string{}
			if slices.ContainsFunc(def.Indexes, func(ix source.IndexDef) bool { return ix.Name == "PRIMARY" }) {
				used[pgShortName(t.Name+"__PRIMARY")] = "the primary key"
			}
			for _, fk := range def.ForeignKeys {
				skip := func(why string) {
					if own {
						warnings.add(t.Name, "table %s: foreign key %s %s and is not output",
							sqlvalue.QuoteIdent(t.Name), sqlvalue.QuoteIdent(fk.Name), why)
					}
				}
				refDB, dumped := byDB[fk.RefSchema]
				if !dumped {
					skip("references database " + sqlvalue.QuoteIdent(fk.RefSchema) + ", which is not dumped")
					continue
				}
				ref, inDB := refDB.tables[fk.RefTable]
				refHave := refDB.outputs(ref)
				switch {
				case !inDB:
					where := ""
					if refDB.DB != d.DB {
						where = " of database " + sqlvalue.QuoteIdent(refDB.DB)
					}
					skip("references table " + sqlvalue.QuoteIdent(fk.RefTable) + where + ", which is not output")
					continue
				case slices.ContainsFunc(fk.Columns, func(c string) bool { return !have[c] }):
					skip("uses a generated column")
					continue
				case slices.ContainsFunc(fk.RefColumns, func(c string) bool { return !refHave[c] }):
					skip("references a generated column")
					continue
				case !isUniqueKey(refDB.Defs[ref.Name], fk.RefColumns):
					skip("references columns that are not a primary key or unique key")
					continue
				}
				// Only a foreign key that is output has a PostgreSQL name. A failed run
				// prints no warnings, so an error names the name before the cut.
				name, myTable, myFK := pgShortName(fk.Name), sqlvalue.QuoteIdent(t.Name), sqlvalue.QuoteIdent(fk.Name)
				who := "foreign key " + myFK
				if name != fk.Name && own {
					warnings.add(t.Name, "table %s: foreign key %s is named %s in PostgreSQL, not %s (over %d bytes)",
						myTable, myFK, sqlvalue.QuoteIdent(name), myFK, pgMaxIdent)
				}
				if prev, dup := used[name]; dup && own {
					return fmt.Errorf("table %s: the PostgreSQL name %s of %s is already used by %s",
						myTable, sqlvalue.QuoteIdent(name), who, prev)
				}
				used[name] = who
				// The key is written by the database whose constraints file runs later.
				host := d
				if refDB.DB != d.DB && refDB.ConstraintsFile > d.ConstraintsFile {
					host = refDB
				}
				if host.DB != s.DB {
					continue
				}
				out.WriteString("ALTER TABLE " + qualified(d.DB, t.Name) + " ADD CONSTRAINT " + sqlvalue.QuotePGIdent(name) +
					" FOREIGN KEY (" + pgList(fk.Columns) + ") REFERENCES " + qualified(refDB.DB, fk.RefTable) +
					" (" + pgList(fk.RefColumns) + ") ON DELETE " + fk.OnDelete + " ON UPDATE " + fk.OnUpdate + ";\n")
			}
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
		q[i] = pgQuote(n)
	}
	return strings.Join(q, ", ")
}
