package source

import (
	"reflect"
	"testing"

	"mysqldance/internal/mask"
)

func TestCatalogTables(t *testing.T) {
	objects := []Object{
		{Name: "v", View: true},
		{Name: "skip", Excluded: true},
		{
			Name:    "t",
			OrderBy: []string{"id"},
			Def: TableDef{
				Columns: []ColumnDef{
					{Name: "id", Type: "int unsigned", DataType: "int"},
					{Name: "s", Type: "varchar(5)", DataType: "varchar", CharacterSet: "utf8mb4", Nullable: true},
					{Name: "g", Type: "int", DataType: "int", Generated: true},
				},
				Indexes: []IndexDef{{Name: "uq", Unique: true, Parts: []IndexPart{{Column: "s"}, {Column: ""}}}},
				ForeignKeys: []ForeignKey{{
					Name: "fk", Columns: []string{"id"}, RefSchema: "o", RefTable: "p", RefColumns: []string{"pid"},
				}},
			},
		},
	}
	got := CatalogTables(objects)
	if !got[0].View || !got[1].Excluded {
		t.Errorf("view and excluded flags lost: %+v", got[:2])
	}
	want := mask.CatalogTable{
		Name:    "t",
		OrderBy: []string{"id"},
		Columns: []mask.CatalogColumn{
			{Name: "id", TypeName: "UNSIGNED INT", ColumnType: "int unsigned", DataType: "int"},
			{Name: "s", TypeName: "VARCHAR", ColumnType: "varchar(5)", DataType: "varchar", CharacterSet: "utf8mb4", Nullable: true},
			{Name: "g", TypeName: "INT", ColumnType: "int", DataType: "int", Generated: true},
		},
		// The functional key part has no column and is left out.
		Indexes:     []mask.CatalogIndex{{Name: "uq", Unique: true, Columns: []string{"s"}}},
		ForeignKeys: []mask.CatalogFK{{Name: "fk", Columns: []string{"id"}, RefDB: "o", RefTable: "p", RefColumns: []string{"pid"}}},
	}
	if !reflect.DeepEqual(got[2], want) {
		t.Errorf("got  %+v\nwant %+v", got[2], want)
	}
}
