package source

import (
	"context"
	"fmt"

	"mysqldance/internal/mask"
)

// MaskCatalog reads the catalog of every database in dbs, which the masking
// rules are checked against. It reads the catalog only. The connection is left
// on the first database, where Open leaves it.
func (s *Snapshot) MaskCatalog(ctx context.Context, dbs []string) (mask.Catalog, error) {
	fold, err := s.FoldsTableCase(ctx)
	if err != nil {
		return mask.Catalog{}, err
	}
	cat := mask.Catalog{FoldTableCase: fold, Databases: map[string][]mask.CatalogTable{}}
	for i, db := range dbs {
		// Open leaves the first database selected.
		if i > 0 {
			if err := s.Use(ctx, db); err != nil {
				return mask.Catalog{}, fmt.Errorf("database %q: %w", db, err)
			}
		}
		objects, err := s.Objects(ctx)
		if err != nil {
			return mask.Catalog{}, fmt.Errorf("database %q: %w", db, err)
		}
		cat.Databases[db] = CatalogTables(objects)
	}
	if len(dbs) > 1 {
		if err := s.Use(ctx, dbs[0]); err != nil {
			return mask.Catalog{}, fmt.Errorf("database %q: %w", dbs[0], err)
		}
	}
	return cat, nil
}

// CatalogTables turns what the source reads into what the rules are checked
// against.
func CatalogTables(objects []Object) []mask.CatalogTable {
	out := make([]mask.CatalogTable, len(objects))
	for i, o := range objects {
		t := mask.CatalogTable{Name: o.Name, View: o.View, Excluded: o.Excluded, OrderBy: o.OrderBy}
		for _, c := range o.Def.Columns {
			t.Columns = append(t.Columns, mask.CatalogColumn{
				Name: c.Name, TypeName: CatalogTypeName(c.DataType, c.Type),
				ColumnType: c.Type, DataType: c.DataType, CharacterSet: c.CharacterSet,
				Nullable: c.Nullable, Generated: c.Generated,
			})
		}
		for _, ix := range o.Def.Indexes {
			ci := mask.CatalogIndex{Name: ix.Name, Unique: ix.Unique}
			for _, p := range ix.Parts {
				if p.Column != "" { // a functional key part has no column
					ci.Columns = append(ci.Columns, p.Column)
				}
			}
			t.Indexes = append(t.Indexes, ci)
		}
		for _, fk := range o.Def.ForeignKeys {
			t.ForeignKeys = append(t.ForeignKeys, mask.CatalogFK{
				Name: fk.Name, Columns: fk.Columns, RefDB: fk.RefSchema, RefTable: fk.RefTable, RefColumns: fk.RefColumns,
			})
		}
		out[i] = t
	}
	return out
}
