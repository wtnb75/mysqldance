package dump

import (
	"context"
	"fmt"

	"mysqldance/internal/mask"
	"mysqldance/internal/source"
)

// resolveMask checks the masking rules of opt against the catalog of every
// database to dump, and returns the plan that transforms the rows. It reads the
// catalog only. The connection is left on the first database, where Run
// expects it.
func resolveMask(ctx context.Context, opt Options, snap *source.Snapshot) (*mask.Plan, error) {
	fold, err := snap.FoldsTableCase(ctx)
	if err != nil {
		return nil, err
	}
	cat := mask.Catalog{FoldTableCase: fold, Databases: map[string][]mask.CatalogTable{}}
	for i, db := range opt.DBs {
		// Open leaves the first database selected.
		if i > 0 {
			if err := snap.Use(ctx, db); err != nil {
				return nil, fmt.Errorf("database %q: %w", db, err)
			}
		}
		objects, err := snap.Objects(ctx)
		if err != nil {
			return nil, fmt.Errorf("database %q: %w", db, err)
		}
		cat.Databases[db] = catalogTables(objects)
	}
	if len(opt.DBs) > 1 {
		if err := snap.Use(ctx, opt.DBs[0]); err != nil {
			return nil, fmt.Errorf("database %q: %w", opt.DBs[0], err)
		}
	}
	plan, err := opt.Mask.Resolve(cat, mask.DefaultRegistry(), opt.MaskSalt)
	if err != nil {
		return nil, fmt.Errorf("mask rules: %w", err)
	}
	if opt.StrictColumns {
		if err := opt.Mask.CheckStrictColumns(cat); err != nil {
			return nil, fmt.Errorf("mask rules: %w", err)
		}
	}
	return plan, nil
}

// catalogTables turns what the source reads into what the rules are checked
// against.
func catalogTables(objects []source.Object) []mask.CatalogTable {
	out := make([]mask.CatalogTable, len(objects))
	for i, o := range objects {
		t := mask.CatalogTable{Name: o.Name, View: o.View, Excluded: o.Excluded, OrderBy: o.OrderBy}
		for _, c := range o.Def.Columns {
			t.Columns = append(t.Columns, mask.CatalogColumn{
				Name: c.Name, TypeName: source.CatalogTypeName(c.DataType, c.Type),
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
