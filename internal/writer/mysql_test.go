package writer

import (
	"bytes"
	"slices"
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

// A view that reads another database restores only if that database is already
// there (B-023): the output is unchanged, and the dump says so for each view.
func TestMySQLWarnsAboutViewsReadingAnotherDatabase(t *testing.T) {
	w := NewMySQL(datafile.Options{})
	views := []schemafile.View{
		{Name: "a", Create: "CREATE VIEW `a` AS select 1"},
		{Name: "b", Create: "CREATE VIEW `b` AS select 1 from `x`.`t`, `y`.`t`", Reads: []string{"x", "y"}},
		{Name: "c", Create: "CREATE VIEW `c` AS select 1 from `x`.`t`", Reads: []string{"x"}},
	}
	var warns []string
	var got bytes.Buffer
	err := w.WriteSchema(&got, Schema{DB: "d", CreateDatabase: "CREATE DATABASE `d`", Views: views,
		Warn: func(msg string) { warns = append(warns, msg) }})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"view `b` reads database `x`: it must be restored before this one (files restore in file name order), or the CREATE VIEW fails",
		"view `b` reads database `y`: it must be restored before this one (files restore in file name order), or the CREATE VIEW fails",
		"view `c` reads database `x`: it must be restored before this one (files restore in file name order), or the CREATE VIEW fails",
	}
	if !slices.Equal(warns, want) {
		t.Errorf("warnings = %q, want %q", warns, want)
	}

	// The warnings do not change the file, and a nil Warn is allowed.
	var quiet bytes.Buffer
	if err := w.WriteSchema(&quiet, Schema{DB: "d", CreateDatabase: "CREATE DATABASE `d`", Views: views}); err != nil {
		t.Fatal(err)
	}
	if got.String() != quiet.String() {
		t.Errorf("Warn changed the output:\n got: %q\nwant: %q", got.String(), quiet.String())
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

// Routine bodies are MySQL syntax too: only the MySQL writer carries them.
var _ RoutineWriter = (*MySQL)(nil)

func TestOnlyMySQLWritesRoutines(t *testing.T) {
	for name, w := range map[string]Writer{"postgres": NewPostgres(datafile.Options{}), "sqlite": NewSQLite(datafile.Options{})} {
		if _, ok := w.(RoutineWriter); ok {
			t.Errorf("%s writer implements RoutineWriter", name)
		}
	}
}

// Event bodies are MySQL syntax too: only the MySQL writer carries them.
var _ EventWriter = (*MySQL)(nil)

func TestOnlyMySQLWritesEvents(t *testing.T) {
	for name, w := range map[string]Writer{"postgres": NewPostgres(datafile.Options{}), "sqlite": NewSQLite(datafile.Options{})} {
		if _, ok := w.(EventWriter); ok {
			t.Errorf("%s writer implements EventWriter", name)
		}
	}
}

func TestMySQLEventsWithoutDefiner(t *testing.T) {
	w := NewMySQL(datafile.Options{})
	events := []schemafile.Event{
		{Name: "e", SQLMode: "ANSI_QUOTES", TimeZone: "+09:00",
			Create: "CREATE DEFINER=`x`@`%` EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1"},
	}
	var got bytes.Buffer
	if err := w.WriteEvents(&got, Schema{DB: "d", Events: events}); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	err := schemafile.WriteEvents(&want, "d", []schemafile.Event{
		{Name: "e", SQLMode: "ANSI_QUOTES", TimeZone: "+09:00",
			Create: "CREATE EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("events differ:\n got: %q\nwant: %q", got.String(), want.String())
	}
	if strings.Contains(got.String(), "DEFINER") {
		t.Errorf("DEFINER left in %q", got.String())
	}
}

func TestMySQLRoutinesWithoutDefiner(t *testing.T) {
	w := NewMySQL(datafile.Options{})
	routines := []schemafile.Routine{
		{Name: "f", Type: "FUNCTION", SQLMode: "ANSI_QUOTES",
			Create: "CREATE DEFINER=`x`@`%` FUNCTION `f`() RETURNS int RETURN 1"},
	}
	var got bytes.Buffer
	if err := w.WriteRoutines(&got, Schema{DB: "d", Routines: routines}); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	err := schemafile.WriteRoutines(&want, "d", []schemafile.Routine{
		{Name: "f", Type: "FUNCTION", SQLMode: "ANSI_QUOTES",
			Create: "CREATE FUNCTION `f`() RETURNS int RETURN 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("routines differ:\n got: %q\nwant: %q", got.String(), want.String())
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
