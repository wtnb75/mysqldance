package dump

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/mask"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
	"mysqldance/internal/writer"
)

// maskingRows is fakeRows for a source that applies the mask of the table, as
// source.Snapshot.EachRow does.
type maskingRows struct{ fakeRows }

func (m maskingRows) EachRow(ctx context.Context, t source.TableInfo, fn func([]sqlvalue.Value) error) error {
	return m.fakeRows.EachRow(ctx, t, func(row []sqlvalue.Value) error {
		if t.Mask != nil {
			if err := t.Mask(row); err != nil {
				return err
			}
		}
		return fn(row)
	})
}

func streamCatalog() mask.Catalog {
	return mask.Catalog{Databases: map[string][]mask.CatalogTable{"d": {{
		Name: "t",
		Columns: []mask.CatalogColumn{
			{Name: "id", TypeName: "INT", ColumnType: "int", DataType: "int"},
			{Name: "s", TypeName: "VARCHAR", ColumnType: "varchar(40)", DataType: "varchar", CharacterSet: "utf8mb4", Nullable: true},
		},
		OrderBy: []string{"id"},
		Indexes: []mask.CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}}}}
}

// writeTable hands the mask of the plan to the reader, so that what the writer
// gets is the masked row, and a table without a rule gets none.
func TestWriteTableAppliesTheMaskPlan(t *testing.T) {
	reg := mask.DefaultRegistry()
	rules, err := mask.Parse([]byte("version: 1\nrules:\n  d.t.s:\n    use: fixed\n    value: hidden\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := rules.Resolve(streamCatalog(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	opt := Options{OutDir: dir, Log: testLogger(io.Discard), Writer: writer.NewMySQL(datafile.Options{}), maskPlan: plan}
	st := &stage{dir: dir}
	res, err := writeTable(context.Background(), opt, maskingRows{fakeRows{n: 3}}, naming.NewPlan("d", []string{"t"}), "d", streamTable, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.commit(); err != nil {
		t.Fatal(err)
	}
	b := readFiles(t, dir)[res.names[0]]
	if strings.Contains(b, "row ") || strings.Count(b, "'hidden'") != 3 {
		t.Errorf("the writer did not get the masked rows:\n%s", b)
	}

	// The same table without a plan is written as it is read.
	dir = t.TempDir()
	opt = Options{OutDir: dir, Log: testLogger(io.Discard), Writer: writer.NewMySQL(datafile.Options{})}
	st = &stage{dir: dir}
	res, err = writeTable(context.Background(), opt, maskingRows{fakeRows{n: 3}}, naming.NewPlan("d", []string{"t"}), "d", streamTable, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.commit(); err != nil {
		t.Fatal(err)
	}
	if b := readFiles(t, dir)[res.names[0]]; !strings.Contains(b, "'row 1'") {
		t.Errorf("a run without rules changed the rows:\n%s", b)
	}
}

// A mask that fails fails the table with an error that holds no value.
func TestWriteTableMaskFailure(t *testing.T) {
	reg := mask.NewRegistry()
	if err := reg.Register(mask.Transform{Name: "boom", Build: func(mask.BuildContext) (mask.Func, error) {
		return func(mask.Input) (sqlvalue.Value, error) { return sqlvalue.Value{}, fmt.Errorf("cannot transform") }, nil
	}}); err != nil {
		t.Fatal(err)
	}
	rules, err := mask.Parse([]byte("version: 1\nrules:\n  d.t.s:\n    use: boom\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := rules.Resolve(streamCatalog(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	opt := Options{OutDir: dir, Log: testLogger(io.Discard), Writer: writer.NewMySQL(datafile.Options{}), maskPlan: plan}
	st := &stage{dir: dir}
	_, err = writeTable(context.Background(), opt, maskingRows{fakeRows{n: 3}}, naming.NewPlan("d", []string{"t"}), "d", streamTable, st)
	if err == nil || !strings.Contains(err.Error(), "cannot transform") || strings.Contains(err.Error(), "row ") {
		t.Fatalf("err = %v", err)
	}
	if err := st.discard(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogTables(t *testing.T) {
	objects := []source.Object{
		{Name: "v", View: true},
		{Name: "skip", Excluded: true},
		{
			Name:    "t",
			OrderBy: []string{"id"},
			Def: source.TableDef{
				Columns: []source.ColumnDef{
					{Name: "id", Type: "int unsigned", DataType: "int"},
					{Name: "s", Type: "varchar(5)", DataType: "varchar", CharacterSet: "utf8mb4", Nullable: true},
					{Name: "g", Type: "int", DataType: "int", Generated: true},
				},
				Indexes: []source.IndexDef{{Name: "uq", Unique: true, Parts: []source.IndexPart{{Column: "s"}, {Column: ""}}}},
				ForeignKeys: []source.ForeignKey{{
					Name: "fk", Columns: []string{"id"}, RefSchema: "o", RefTable: "p", RefColumns: []string{"pid"},
				}},
			},
		},
	}
	got := catalogTables(objects)
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

// maskTestRules is a rule file for the database db.
func maskTestRules(t *testing.T, db string) *mask.Rules {
	t.Helper()
	src := fmt.Sprintf("version: 1\nrules:\n  %[1]s.users.email:\n    use: fixed\n    value: masked@example.invalid\n"+
		"  %[1]s.users.age:\n    use: nullify\n  %[1]s.logs.msg:\n    use: fixed\n    value: hidden\n", db)
	rules, err := mask.Parse([]byte(src), mask.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func queryRows(t *testing.T, e *env, query string) []string {
	t.Helper()
	rows, err := e.admin.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		cells := make([]sql.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range cells {
			dest[i] = &cells[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = c.String
			if !c.Valid {
				parts[i] = "NULL"
			}
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// With rules, the files hold only the transformed rows, whatever --target
// would do with them, the same bytes come out of every run (twice, with one
// reader and with two, with the sort in memory and through scratch files), and a
// restore changes the columns that have a rule and nothing else.
func TestMaskDump(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE users (id INT PRIMARY KEY, email VARCHAR(40) NOT NULL, age INT NULL, note VARCHAR(30) NULL)",
		"INSERT INTO users VALUES (1,'alice-RAW@x.test',31,'n1'),(2,'bob-RAW@x.test',NULL,NULL),(3,'carol-RAW@x.test',45,'n3')",
		"CREATE TABLE logs (msg VARCHAR(40), n INT)",
		"INSERT INTO logs VALUES ('zRAW',1),('aRAW',2),('mRAW',3),('aRAW',2)",
	)
	run := func(jobs int, sortMemory int64, tgt writer.Writer) (map[string]string, string) {
		t.Helper()
		dir := t.TempDir()
		cfg := e.cfg
		cfg.Jobs = jobs
		var log bytes.Buffer
		opt := Options{Source: cfg, DBs: []string{db}, OutDir: dir, Log: testLogger(&log), Mask: maskTestRules(t, db), SortMemory: sortMemory, Writer: tgt}
		if err := Run(context.Background(), opt); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return readFiles(t, dir), log.String()
	}
	for name, mk := range map[string]func() writer.Writer{
		"mysql":    func() writer.Writer { return writer.NewMySQL(datafile.Options{}) },
		"sqlite":   func() writer.Writer { return writer.NewSQLite(datafile.Options{}) },
		"postgres": func() writer.Writer { return writer.NewPostgres(datafile.Options{}) },
	} {
		t.Run(name, func(t *testing.T) {
			first, log := run(1, 0, mk())
			for _, c := range []struct {
				name       string
				jobs       int
				sortMemory int64
			}{{"again", 1, 0}, {"jobs 2", 2, 0}, {"spilled sort", 1, 1}} {
				got, _ := run(c.jobs, c.sortMemory, mk())
				if !reflect.DeepEqual(got, first) {
					t.Errorf("%s: the files differ from the first run", c.name)
				}
			}
			for file, text := range first {
				if strings.Contains(text, "RAW") || strings.Contains(text, "alice") || strings.Contains(text, "@x.test") {
					t.Errorf("%s holds a raw value", file)
				}
			}
			if strings.Contains(log, "RAW") || strings.Contains(log, "masked@example.invalid") {
				t.Errorf("the log holds a value:\n%s", log)
			}
			if !strings.Contains(strings.Join(slices.Collect(maps.Values(first)), "\n"), "masked@example.invalid") {
				t.Error("the fixed value is not in the output")
			}
		})
	}

	// Restore the MySQL files over the original tables: the masked columns change,
	// everything else stays.
	files, _ := run(1, 0, writer.NewMySQL(datafile.Options{}))
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.restore(t, dir)
	wantUsers := []string{"1|masked@example.invalid|NULL|n1", "2|masked@example.invalid|NULL|NULL", "3|masked@example.invalid|NULL|n3"}
	if got := queryRows(t, e, "SELECT id, email, age, note FROM users ORDER BY id"); !reflect.DeepEqual(got, wantUsers) {
		t.Errorf("users = %v, want %v", got, wantUsers)
	}
	wantLogs := []string{"hidden|1", "hidden|2", "hidden|2", "hidden|3"}
	if got := queryRows(t, e, "SELECT msg, n FROM logs ORDER BY n"); !reflect.DeepEqual(got, wantLogs) {
		t.Errorf("logs = %v, want %v", got, wantLogs)
	}
}

// A wrong rule fails the run before the output directory is touched, and every
// wrong rule is reported at once.
func TestMaskRulesAreCheckedBeforeTheOutputDirectory(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE parent (id INT PRIMARY KEY, name VARCHAR(10) NOT NULL)",
		"CREATE TABLE child (id INT PRIMARY KEY, pid INT NOT NULL, code VARCHAR(10) NOT NULL, UNIQUE KEY uq (code), FOREIGN KEY (pid) REFERENCES parent (id))",
	)
	src := fmt.Sprintf("version: 1\nrules:\n"+
		"  %[1]s.parent.id:\n    use: nullify\n"+ // referenced by child
		"  %[1]s.child.id:\n    use: fixed\n    value: 1\n"+ // the primary key
		"  %[1]s.child.pid:\n    use: fixed\n    value: 1\n"+ // the child side of a foreign key
		"  %[1]s.child.code:\n    use: fixed\n    value: x\n"+ // a unique index
		"  %[1]s.parent.name:\n    use: fixed\n    value: toolongforvarchar10\n"+ // does not fit
		"  %[1]s.parent.nope:\n    use: nullify\n", db) // no such column
	rules, err := mask.Parse([]byte(src), mask.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "new", "out")
	err = Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: missing, Mask: rules, Log: testLogger(io.Discard)})
	if err == nil {
		t.Fatal("Run succeeded")
	}
	for _, want := range []string{
		"referenced by a foreign key of table " + db + ".child",
		`column "id" of table "child"`,
		"part of the foreign key", // the server names the key
		`unique index "uq"`,
		"longer than 10 characters",
		`no such column "nope"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
	if exists(t, filepath.Dir(missing)) {
		t.Errorf("%s was created by a failed run", filepath.Dir(missing))
	}
}

// StrictColumns without rules is refused before anything is opened.
func TestStrictColumnsNeedsRules(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "out")
	err := Run(context.Background(), Options{DBs: []string{"d"}, OutDir: missing, StrictColumns: true})
	if err == nil || !strings.Contains(err.Error(), "StrictColumns needs Mask") {
		t.Fatalf("err = %v", err)
	}
	if exists(t, missing) {
		t.Error("the output directory was created")
	}
}

// With --strict-columns every output column needs a decision, the columns
// without one are listed by name, and the output directory is not touched.
// Without it the same rules dump as they always did.
func TestStrictColumns(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t,
		"CREATE TABLE users (id INT PRIMARY KEY, email VARCHAR(40) NOT NULL, note VARCHAR(30) NULL, email_lower VARCHAR(40) AS (LOWER(email)) STORED)",
		"INSERT INTO users (id, email, note) VALUES (1,'alice-RAW@x.test','n1')",
		"CREATE TABLE logs (msg VARCHAR(40), n INT)",
		"CREATE TABLE skipped (secret VARCHAR(10))",
		"CREATE VIEW v_users AS SELECT id FROM users",
	)
	parse := func(body string) *mask.Rules {
		t.Helper()
		rules, err := mask.Parse([]byte("version: 1\nrules:\n"+strings.ReplaceAll(body, "DB", db)), mask.DefaultRegistry())
		if err != nil {
			t.Fatal(err)
		}
		return rules
	}
	run := func(out string, rules *mask.Rules, strict bool) error {
		return Run(context.Background(), Options{
			Source: e.cfg, DBs: []string{db}, OutDir: out, Mask: rules, StrictColumns: strict,
			ExcludeTables: []string{db + ".skipped"}, Log: testLogger(io.Discard),
		})
	}

	partial := parse("  DB.users.email:\n    use: fixed\n    value: masked@example.invalid\n  DB.users.id:\n    use: keep\n")
	missing := filepath.Join(t.TempDir(), "new", "out")
	err := run(missing, partial, true)
	if err == nil {
		t.Fatal("Run succeeded with undecided columns")
	}
	for _, want := range []string{db + ".users.note", db + ".logs.msg", db + ".logs.n"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %s:\n%v", want, err)
		}
	}
	for _, no := range []string{db + ".users.id", db + ".users.email", "email_lower", "skipped", "v_users", "RAW"} {
		if strings.Contains(err.Error(), no) {
			t.Errorf("the error mentions %q:\n%v", no, err)
		}
	}
	if exists(t, filepath.Dir(missing)) {
		t.Errorf("%s was created by a failed run", filepath.Dir(missing))
	}

	// Without the flag the same rules dump what they name, and nothing else changes.
	dir := t.TempDir()
	if err := run(dir, partial, false); err != nil {
		t.Fatalf("without --strict-columns: %v", err)
	}
	all := ""
	files := readFiles(t, dir)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		all += files[name]
	}
	if strings.Contains(all, "RAW") || !strings.Contains(all, "masked@example.invalid") || !strings.Contains(all, "'n1'") {
		t.Errorf("the rows are not masked as the rules say:\n%s", all)
	}

	// A decision for every column, wildcard included, lets the dump through.
	full := parse("  DB.users.email:\n    use: fixed\n    value: masked@example.invalid\n  DB.users.*:\n    use: keep\n  DB.logs.*:\n    use: keep\n")
	if err := run(t.TempDir(), full, true); err != nil {
		t.Fatalf("every column decided: %v", err)
	}
}

// With several databases the rules are checked against all of them, and the
// dump still starts on the first one.
func TestMaskWithSeveralDatabases(t *testing.T) {
	e := newEnv(t)
	db1, db2 := uniqueName(t), uniqueName(t)
	e.createDB(t, db1)
	e.exec(t, "CREATE TABLE a (id INT PRIMARY KEY, s VARCHAR(20))", "INSERT INTO a VALUES (1,'RAW1')")
	e.createDB(t, db2)
	e.exec(t, "CREATE TABLE b (id INT PRIMARY KEY, s VARCHAR(20))", "INSERT INTO b VALUES (1,'RAW2')")
	src := fmt.Sprintf("version: 1\nrules:\n  %s.a.s:\n    use: nullify\n  %s.b.s:\n    use: fixed\n    value: other\n", db1, db2)
	rules, err := mask.Parse([]byte(src), mask.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db1, db2}, OutDir: dir, Mask: rules, Log: testLogger(io.Discard)}); err != nil {
		t.Fatal(err)
	}
	files := readFiles(t, dir)
	var all string
	for _, name := range slices.Sorted(maps.Keys(files)) {
		all += files[name]
	}
	if strings.Contains(all, "RAW") || !strings.Contains(all, "'other'") || !strings.Contains(all, "NULL") {
		t.Errorf("the rows are not masked as the rules say:\n%s", all)
	}
}
