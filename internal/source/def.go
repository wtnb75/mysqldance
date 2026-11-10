package source

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strings"
)

// TableDef is the structure of a base table as the catalog reports it: what a
// writer for another DBMS needs to build its own table definition, where
// TableInfo.Create is MySQL text only.
type TableDef struct {
	// Columns lists all columns in ordinal order, generated ones included.
	Columns []ColumnDef
	// Indexes lists the indexes sorted by name, the primary key (named
	// PRIMARY) among them.
	Indexes []IndexDef
	// ForeignKeys lists the foreign keys sorted by name.
	ForeignKeys []ForeignKey
	// Checks lists the names of the CHECK constraints, sorted. Their
	// expressions are MySQL syntax and are not read.
	Checks []string
}

// ColumnDef is one column of a TableDef.
type ColumnDef struct {
	Name string
	// Type is COLUMN_TYPE (e.g. "int unsigned", "decimal(10,2)"); DataType is
	// DATA_TYPE (e.g. "int", "decimal").
	Type, DataType string
	Nullable       bool
	// Default is COLUMN_DEFAULT, or nil if the column has none. When
	// DefaultExpr is true it is the text of a default expression (or
	// CURRENT_TIMESTAMP), otherwise the default value itself, unquoted.
	Default       *string
	DefaultExpr   bool
	OnUpdate      bool // ON UPDATE CURRENT_TIMESTAMP
	AutoIncrement bool
	Generated     bool // VIRTUAL or STORED GENERATED
}

// IndexDef is one index of a TableDef.
type IndexDef struct {
	Name   string
	Unique bool
	// Kind is INDEX_TYPE: BTREE, HASH, FULLTEXT or SPATIAL.
	Kind  string
	Parts []IndexPart
}

// IndexPart is one key part of an IndexDef.
type IndexPart struct {
	// Column is empty for a functional key part.
	Column string
	Desc   bool
	// Prefix is true when only a prefix of the column is indexed.
	Prefix bool
}

// ForeignKey is one foreign key of a TableDef.
type ForeignKey struct {
	Name                string
	Columns             []string
	RefSchema, RefTable string
	RefColumns          []string
	// OnUpdate and OnDelete are the referential actions: RESTRICT, NO ACTION,
	// CASCADE, SET NULL or SET DEFAULT.
	OnUpdate, OnDelete string
}

// Defs returns the structure of every base table of the current database,
// keyed by table name. It reads the catalog only; Tables and Rows do not
// depend on it.
func (s *Snapshot) Defs(ctx context.Context) (map[string]TableDef, error) {
	list, err := s.listTables(ctx)
	if err != nil {
		return nil, err
	}
	// Pointers, so that the readers below append in place.
	defs := map[string]*TableDef{}
	for _, r := range list {
		if r.typ == "BASE TABLE" {
			defs[r.name] = &TableDef{}
		}
	}
	for _, read := range []func(context.Context, map[string]*TableDef) error{
		s.defColumns, s.defIndexes, s.defForeignKeys, s.defChecks,
	} {
		if err := read(ctx, defs); err != nil {
			return nil, err
		}
	}
	out := make(map[string]TableDef, len(defs))
	for name, d := range defs {
		// Byte order, whatever collation the catalog sorted by.
		slices.SortFunc(d.Indexes, func(a, b IndexDef) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortFunc(d.ForeignKeys, func(a, b ForeignKey) int { return cmp.Compare(a.Name, b.Name) })
		slices.Sort(d.Checks)
		out[name] = *d
	}
	return out, nil
}

func (s *Snapshot) defColumns(ctx context.Context, defs map[string]*TableDef) error {
	rows, err := s.conn.QueryContext(ctx, s.dialect.defColumns, s.name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, nullable, extra string
		var def sql.NullString
		var c ColumnDef
		if err := rows.Scan(&table, &c.Name, &c.DataType, &c.Type, &nullable, &def, &extra); err != nil {
			return err
		}
		d, ok := defs[table]
		if !ok {
			continue
		}
		c.Nullable = nullable == "YES"
		if def.Valid {
			c.Default = &def.String
		}
		lower := strings.ToLower(extra)
		c.DefaultExpr = strings.Contains(lower, "default_generated")
		c.OnUpdate = strings.Contains(lower, "on update")
		c.AutoIncrement = strings.Contains(lower, "auto_increment")
		c.Generated = strings.Contains(lower, "virtual generated") || strings.Contains(lower, "stored generated")
		d.Columns = append(d.Columns, c)
	}
	return rows.Err()
}

func (s *Snapshot) defIndexes(ctx context.Context, defs map[string]*TableDef) error {
	rows, err := s.conn.QueryContext(ctx, s.dialect.defIndexes, s.name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, name, kind string
		var nonUnique int
		var col, collation sql.NullString
		var sub sql.NullInt64
		if err := rows.Scan(&table, &name, &nonUnique, &kind, &col, &collation, &sub); err != nil {
			return err
		}
		d, ok := defs[table]
		if !ok {
			continue
		}
		part := IndexPart{Column: col.String, Desc: collation.String == "D", Prefix: sub.Valid}
		if n := len(d.Indexes); n > 0 && d.Indexes[n-1].Name == name {
			d.Indexes[n-1].Parts = append(d.Indexes[n-1].Parts, part)
		} else {
			d.Indexes = append(d.Indexes, IndexDef{Name: name, Unique: nonUnique == 0, Kind: kind, Parts: []IndexPart{part}})
		}
	}
	return rows.Err()
}

func (s *Snapshot) defForeignKeys(ctx context.Context, defs map[string]*TableDef) error {
	rows, err := s.conn.QueryContext(ctx, s.dialect.defForeignKeys, s.name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, name, col, refSchema, refTable, refCol, onUpdate, onDelete string
		if err := rows.Scan(&table, &name, &col, &refSchema, &refTable, &refCol, &onUpdate, &onDelete); err != nil {
			return err
		}
		d, ok := defs[table]
		if !ok {
			continue
		}
		if n := len(d.ForeignKeys); n > 0 && d.ForeignKeys[n-1].Name == name {
			fk := &d.ForeignKeys[n-1]
			fk.Columns = append(fk.Columns, col)
			fk.RefColumns = append(fk.RefColumns, refCol)
		} else {
			d.ForeignKeys = append(d.ForeignKeys, ForeignKey{
				Name: name, Columns: []string{col}, RefSchema: refSchema, RefTable: refTable,
				RefColumns: []string{refCol}, OnUpdate: onUpdate, OnDelete: onDelete,
			})
		}
	}
	return rows.Err()
}

func (s *Snapshot) defChecks(ctx context.Context, defs map[string]*TableDef) error {
	rows, err := s.conn.QueryContext(ctx, s.dialect.defChecks, s.name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			return err
		}
		if d, ok := defs[table]; ok {
			d.Checks = append(d.Checks, name)
		}
	}
	return rows.Err()
}
