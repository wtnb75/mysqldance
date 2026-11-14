package writer

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// fkParentKeySchema has a parent whose columns are keyed in several ways and a
// child with one foreign key per case.
func fkParentKeySchema() Schema {
	cols := func(names ...string) []sqlvalue.Column {
		out := make([]sqlvalue.Column, len(names))
		for i, n := range names {
			out[i] = sqlvalue.Column{Name: n, TypeName: "INT"}
		}
		return out
	}
	colDefs := func(names ...string) []source.ColumnDef {
		out := make([]source.ColumnDef, len(names))
		for i, n := range names {
			out[i] = source.ColumnDef{Name: n, Type: "int", DataType: "int", Nullable: n != "id"}
		}
		return out
	}
	parts := func(names ...string) []source.IndexPart {
		out := make([]source.IndexPart, len(names))
		for i, n := range names {
			out[i] = source.IndexPart{Column: n}
		}
		return out
	}
	fk := func(name string, from []string, to ...string) source.ForeignKey {
		return source.ForeignKey{Name: name, Columns: from, RefSchema: "d", RefTable: "parent", RefColumns: to,
			OnUpdate: "NO ACTION", OnDelete: "NO ACTION"}
	}
	return Schema{
		DB: "d",
		Tables: []source.TableInfo{
			{Name: "child", Columns: cols("id", "a", "b", "c", "d", "e")},
			{Name: "parent", Columns: cols("id", "a", "b", "c", "d", "e")},
		},
		Defs: map[string]source.TableDef{
			"parent": {
				Columns: colDefs("id", "a", "b", "c", "d", "e"),
				Indexes: []source.IndexDef{
					{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: parts("id")},
					{Name: "idx_c", Kind: "BTREE", Parts: parts("c")},
					{Name: "uk_ab", Unique: true, Kind: "BTREE", Parts: parts("a", "b")},
					// A unique index with an expression part is not output.
					{Name: "uk_expr", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "e"}, {}}},
				},
			},
			"child": {
				Columns: colDefs("id", "a", "b", "c", "d", "e"),
				Indexes: []source.IndexDef{{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: parts("id")}},
				ForeignKeys: []source.ForeignKey{
					fk("fk_pk", []string{"a"}, "id"),
					fk("fk_unique_swapped", []string{"a", "b"}, "b", "a"),
					fk("fk_non_unique", []string{"c"}, "c"),
					fk("fk_unindexed", []string{"d"}, "d"),
					fk("fk_part_of_unique", []string{"a"}, "a"),
					fk("fk_expression_index", []string{"e"}, "e"),
				},
			},
		},
	}
}

func TestSQLiteForeignKeyParentKey(t *testing.T) {
	var warnings []string
	schema := fkParentKeySchema()
	schema.Warn = func(msg string) { warnings = append(warnings, msg) }
	var got bytes.Buffer
	if err := NewSQLite(datafile.Options{}).WriteSchema(&got, schema); err != nil {
		t.Fatal(err)
	}
	ddl := got.String()

	// A key that is the parent's primary key or a unique index, in any column
	// order, is carried over as before.
	for _, want := range []string{
		"FOREIGN KEY (`a`) REFERENCES `parent` (`id`) ON DELETE NO ACTION ON UPDATE NO ACTION",
		"FOREIGN KEY (`a`, `b`) REFERENCES `parent` (`b`, `a`) ON DELETE NO ACTION ON UPDATE NO ACTION",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("schema lacks %q:\n%s", want, ddl)
		}
	}
	// Any other key would fail with "foreign key mismatch" once foreign keys are
	// enabled, so it is left out with a warning that says why.
	log := strings.Join(warnings, "\n")
	if n := strings.Count(ddl, "FOREIGN KEY"); n != 2 {
		t.Errorf("%d foreign keys in the schema, want 2:\n%s", n, ddl)
	}
	for _, name := range []string{"fk_non_unique", "fk_unindexed", "fk_part_of_unique", "fk_expression_index"} {
		want := "table `child`: foreign key `" + name + "` is not output: " +
			"its columns in `parent` are not the primary key or a unique index"
		if !strings.Contains(log, want) {
			t.Errorf("warnings lack %q:\n%s", want, log)
		}
	}
	for _, name := range []string{"fk_pk", "fk_unique_swapped"} {
		if strings.Contains(log, name) {
			t.Errorf("a foreign key into a unique key was reported: %s", name)
		}
	}

	// The schema restores, and with foreign keys on a child row is accepted.
	db := memDB(t)
	sqliteExec(t, db, ddl)
	sqliteExec(t, db, "PRAGMA foreign_keys=ON; INSERT INTO `parent` VALUES (1,1,1,1,1,1); "+
		"INSERT INTO `child` VALUES (1,1,1,5,5,5);")
}

func TestSQLiteForeignKeySelfReference(t *testing.T) {
	// A foreign key to the table's own primary key is kept.
	schema := Schema{
		DB:     "d",
		Tables: []source.TableInfo{{Name: "node", Columns: []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: "up", TypeName: "INT"}}}},
		Defs: map[string]source.TableDef{"node": {
			Columns: []source.ColumnDef{{Name: "id", Type: "int", DataType: "int"}, {Name: "up", Type: "int", DataType: "int", Nullable: true}},
			Indexes: []source.IndexDef{{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id"}}}},
			ForeignKeys: []source.ForeignKey{{Name: "fk_up", Columns: []string{"up"}, RefSchema: "d", RefTable: "node",
				RefColumns: []string{"id"}, OnUpdate: "NO ACTION", OnDelete: "NO ACTION"}},
		}},
	}
	schema.Warn = func(msg string) { t.Errorf("unexpected warning: %s", msg) }
	var got bytes.Buffer
	if err := NewSQLite(datafile.Options{}).WriteSchema(&got, schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "FOREIGN KEY (`up`) REFERENCES `node` (`id`)") {
		t.Errorf("the self reference is missing:\n%s", got.String())
	}
}
