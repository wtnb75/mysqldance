package writer

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

func intCol(name string) sqlvalue.Column { return sqlvalue.Column{Name: name, TypeName: "INT"} }

func intRows(n int) [][]sqlvalue.Value {
	rows := make([][]sqlvalue.Value, n)
	for i := range rows {
		rows[i] = []sqlvalue.Value{sqlvalue.NewValue("INT", []byte(strconv.Itoa(i)), false)}
	}
	return rows
}

// The MySQL writer must be a Writer.
var _ Writer = (*MySQL)(nil)

func TestMySQLSchemaMatchesSchemafile(t *testing.T) {
	w := NewMySQL(datafile.Options{})
	tables := []source.TableInfo{{Name: "t", Create: "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB AUTO_INCREMENT=7"}}
	views := []schemafile.View{
		{Name: "b", Create: "CREATE DEFINER=`x`@`%` VIEW `b` AS select * from `a`"},
		{Name: "a", Create: "CREATE DEFINER=`x`@`%` VIEW `a` AS select 1"},
	}
	deps := map[string][]string{"b": {"a"}}

	var got bytes.Buffer
	if err := w.WriteSchema(&got, Schema{DB: "d", CreateDatabase: "CREATE DATABASE `d`", Tables: tables, Views: views, ViewDeps: deps}); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	err := schemafile.Write(&want, "d", "CREATE DATABASE `d`",
		[]schemafile.Table{{Name: "t", Create: schemafile.StripAutoIncrement(tables[0].Create)}},
		[]schemafile.View{
			{Name: "a", Create: schemafile.StripDefiner(views[1].Create)},
			{Name: "b", Create: schemafile.StripDefiner(views[0].Create)},
		})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("schema differs:\n got: %q\nwant: %q", got.String(), want.String())
	}
	if strings.Contains(got.String(), "AUTO_INCREMENT=7") || strings.Contains(got.String(), "DEFINER") {
		t.Errorf("AUTO_INCREMENT or DEFINER left in %q", got.String())
	}
}

// The MySQL writer writes triggers. Trigger bodies are MySQL syntax, so the
// PostgreSQL and SQLite writers must not claim to (dump warns for them).
var _ TriggerWriter = (*MySQL)(nil)

func TestOnlyMySQLWritesTriggers(t *testing.T) {
	for name, w := range map[string]Writer{"postgres": NewPostgres(datafile.Options{}), "sqlite": NewSQLite(datafile.Options{})} {
		if _, ok := w.(TriggerWriter); ok {
			t.Errorf("%s writer implements TriggerWriter", name)
		}
	}
}

func TestMySQLTriggersWithoutDefiner(t *testing.T) {
	w := NewMySQL(datafile.Options{})
	triggers := []schemafile.Trigger{
		{Name: "t1", Table: "a", SQLMode: "ANSI_QUOTES",
			Create: "CREATE DEFINER=`x`@`%` TRIGGER `t1` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.x = 1"},
	}
	var got bytes.Buffer
	if err := w.WriteTriggers(&got, Schema{DB: "d", Triggers: triggers}); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	err := schemafile.WriteTriggers(&want, "d", []schemafile.Trigger{
		{Name: "t1", Table: "a", SQLMode: "ANSI_QUOTES",
			Create: "CREATE TRIGGER `t1` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.x = 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("triggers differ:\n got: %q\nwant: %q", got.String(), want.String())
	}
	if strings.Contains(got.String(), "DEFINER") {
		t.Errorf("DEFINER left in %q", got.String())
	}
}

func TestMySQLDataMatchesDatafile(t *testing.T) {
	opt := datafile.Options{RowsPerInsert: 3, RowsPerFile: 4}
	w := NewMySQL(opt)
	tab := source.TableInfo{Name: "t", Columns: []sqlvalue.Column{intCol("id")}, OrderBy: []string{"id"}}
	rows := intRows(40)

	literals := make([][]string, len(rows))
	for i, r := range rows {
		literals[i] = sqlvalue.Literals(r)
	}
	groups := datafile.Split(literals, []int{0}, opt.RowsPerFile)
	parts := dataParts(w, "d", tab, rows)
	if len(groups) < 2 {
		t.Fatalf("test needs a split table, got %d group(s)", len(groups))
	}
	if len(parts) != len(groups) {
		t.Fatalf("parts = %d, want %d", len(parts), len(groups))
	}
	for i, p := range parts {
		var got, want bytes.Buffer
		if err := p(&got); err != nil {
			t.Fatal(err)
		}
		if err := datafile.Write(&want, "d", "t", []string{"id"}, false, groups[i], opt); err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Errorf("part %d differs:\n got: %q\nwant: %q", i, got.String(), want.String())
		}
	}
}

func TestMySQLDataEmptyTable(t *testing.T) {
	w := NewMySQL(datafile.Options{})
	tab := source.TableInfo{Name: "t", Columns: []sqlvalue.Column{intCol("id")}}
	parts := dataParts(w, "d", tab, nil)
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	var got bytes.Buffer
	if err := parts[0](&got); err != nil {
		t.Fatal(err)
	}
	if got.String() != datafile.Header("d") {
		t.Errorf("got %q, want the bare header", got.String())
	}
}
