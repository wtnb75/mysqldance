package scanrun

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/scan"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// fakeReader serves tables and rows from memory and records what is asked of it.
type fakeReader struct {
	tables map[string][]source.TableInfo // by database
	rows   map[string][][]sql.RawBytes   // by "db.table"; a nil cell is NULL
	cur    string
	read   []string // "db.table(col,col)" for each read
	failOn string   // table whose read fails
}

func (f *fakeReader) Use(_ context.Context, db string) error { f.cur = db; return nil }

func (f *fakeReader) Tables(context.Context) ([]source.TableInfo, error) {
	return f.tables[f.cur], nil
}

func (f *fakeReader) EachCells(_ context.Context, table string, cols []string, fn func([]sql.RawBytes) error) error {
	f.read = append(f.read, f.cur+"."+table+"("+strings.Join(cols, ",")+")")
	if table == f.failOn {
		return errors.New("connection lost")
	}
	// The fake keeps the full rows; it hands over only the columns asked for.
	info := f.table(table)
	for _, row := range f.rows[f.cur+"."+table] {
		cells := make([]sql.RawBytes, len(cols))
		for i, c := range cols {
			for j, col := range info.Columns {
				if col.Name == c {
					cells[i] = row[j]
				}
			}
		}
		if err := fn(cells); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeReader) table(name string) source.TableInfo {
	for _, t := range f.tables[f.cur] {
		if t.Name == name {
			return t
		}
	}
	return source.TableInfo{}
}

func cols(pairs ...string) []sqlvalue.Column {
	var out []sqlvalue.Column
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, sqlvalue.Column{Name: pairs[i], TypeName: pairs[i+1]})
	}
	return out
}

func row(cells ...string) []sql.RawBytes {
	out := make([]sql.RawBytes, len(cells))
	for i, c := range cells {
		if c != "\x00" { // "\x00" stands for NULL
			out[i] = sql.RawBytes(c)
		}
	}
	return out
}

func piiConfig(t *testing.T) *scan.Config {
	t.Helper()
	c, err := scan.WithBuiltin("pii")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunCountsRowsAndScannedAndOrdersTheFindings(t *testing.T) {
	f := &fakeReader{
		tables: map[string][]source.TableInfo{"shop": {
			{Name: "users", Columns: cols("id", "INT", "mail", "VARCHAR", "memo", "TEXT")},
		}},
		rows: map[string][][]sql.RawBytes{"shop.users": {
			// Two addresses in one value still count as one row.
			row("1", "a@example.com", "x a@example.com b@example.com"),
			row("2", "\x00", "nothing here"),
			row("3", "c@example.com", "\x00"),
			row("4", "plain", "d@example.com"),
		}},
	}
	got, err := Run(context.Background(), f, piiConfig(t), []string{"shop"})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, g := range got {
		lines = append(lines, g.DB+"."+g.Table+"."+g.Column+" "+g.Rule)
	}
	wantLines := []string{"shop.users.mail pii-email", "shop.users.memo pii-email"}
	if !slices.Equal(lines, wantLines) {
		t.Fatalf("findings = %v, want %v", lines, wantLines)
	}
	if got[0].Rows != 2 || got[0].Scanned != 3 {
		t.Errorf("mail: rows %d scanned %d, want 2 and 3 (a NULL is not scanned)", got[0].Rows, got[0].Scanned)
	}
	if got[1].Rows != 2 || got[1].Scanned != 3 {
		t.Errorf("memo: rows %d scanned %d, want 2 and 3", got[1].Rows, got[1].Scanned)
	}
}

func TestRunSkipsColumnsThatAreNotText(t *testing.T) {
	f := &fakeReader{
		tables: map[string][]source.TableInfo{"shop": {
			{Name: "blobs", Columns: cols("a", "BLOB", "b", "VARBINARY", "c", "BINARY", "d", "BIT", "e", "GEOMETRY", "f", "LONGBLOB")},
			{Name: "mixed", Columns: cols("img", "BLOB", "name", "VARCHAR", "flag", "BIT", "kind", "ENUM", "doc", "JSON", "n", "DECIMAL", "d", "DATETIME")},
		}},
		rows: map[string][][]sql.RawBytes{},
	}
	if _, err := Run(context.Background(), f, piiConfig(t), []string{"shop"}); err != nil {
		t.Fatal(err)
	}
	// The table of binary columns is not read at all, and the others are read
	// without their binary columns.
	want := []string{"shop.mixed(name,kind,doc,n,d)"}
	if !slices.Equal(f.read, want) {
		t.Errorf("reads = %v, want %v", f.read, want)
	}
}

func TestRunUsesEachDatabaseAndTheFullPath(t *testing.T) {
	// A path rule sees DB.TABLE.COLUMN.
	cfg, err := scan.Parse([]byte("[[rules]]\nid = \"by-path\"\npath = '^blog\\.posts\\.author$'\n"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeReader{
		tables: map[string][]source.TableInfo{
			"shop": {{Name: "posts", Columns: cols("author", "VARCHAR")}},
			"blog": {{Name: "posts", Columns: cols("author", "VARCHAR")}},
		},
		rows: map[string][][]sql.RawBytes{
			"shop.posts": {row("x")},
			"blog.posts": {row("y"), row("z")},
		},
	}
	got, err := Run(context.Background(), f, cfg, []string{"shop", "blog"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DB != "blog" || got[0].Rows != 2 || got[0].Scanned != 2 {
		t.Errorf("findings = %+v", got)
	}
	if !slices.Equal(f.read, []string{"shop.posts(author)", "blog.posts(author)"}) {
		t.Errorf("reads = %v", f.read)
	}
}

func TestRunFindingsAreSortedByBytes(t *testing.T) {
	cfg, err := scan.Parse([]byte("[[rules]]\nid = \"b\"\nregex = 'v'\n[[rules]]\nid = \"a\"\nregex = 'v'\n"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeReader{
		tables: map[string][]source.TableInfo{"db2": {{Name: "t", Columns: cols("c", "VARCHAR")}}, "db1": {
			{Name: "t2", Columns: cols("c", "VARCHAR")}, {Name: "T1", Columns: cols("z", "VARCHAR", "a", "VARCHAR")},
		}},
		rows: map[string][][]sql.RawBytes{
			"db2.t": {row("v")}, "db1.t2": {row("v")}, "db1.T1": {row("v", "v")},
		},
	}
	got, err := Run(context.Background(), f, cfg, []string{"db2", "db1"})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, g := range got {
		lines = append(lines, g.DB+"."+g.Table+"."+g.Column+"."+g.Rule)
	}
	want := []string{
		"db1.T1.a.a", "db1.T1.a.b", "db1.T1.z.a", "db1.T1.z.b",
		"db1.t2.c.a", "db1.t2.c.b", "db2.t.c.a", "db2.t.c.b",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("order = %v\nwant    %v", lines, want)
	}
}

func TestRunFailureNamesTheTableAndNoValue(t *testing.T) {
	f := &fakeReader{
		tables: map[string][]source.TableInfo{"shop": {{Name: "users", Columns: cols("mail", "VARCHAR")}}},
		failOn: "users",
	}
	_, err := Run(context.Background(), f, piiConfig(t), []string{"shop"})
	if err == nil || !strings.Contains(err.Error(), "shop.users") || !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("err = %v", err)
	}
}

func sampleFindings() []Finding {
	return []Finding{
		{DB: "shop", Table: "users", Column: "mail", Rule: "pii-email", Rows: 2, Scanned: 3},
		{DB: "shop", Table: "users", Column: "memo", Rule: "pii-email", Rows: 1, Scanned: 40},
	}
}

func TestWriteTable(t *testing.T) {
	var b bytes.Buffer
	if err := WriteTable(&b, sampleFindings()); err != nil {
		t.Fatal(err)
	}
	want := "DATABASE  TABLE  COLUMN  RULE       ROWS  SCANNED\n" +
		"shop      users  mail    pii-email  2     3\n" +
		"shop      users  memo    pii-email  1     40\n"
	if b.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestWriteTableWithoutFindingsHasTheHeader(t *testing.T) {
	var b bytes.Buffer
	if err := WriteTable(&b, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(b.String()); !strings.HasPrefix(got, "DATABASE") || strings.Count(got, "\n") != 0 {
		t.Errorf("output = %q", b.String())
	}
}

func TestWriteJSONIsOneObjectPerLine(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, sampleFindings()); err != nil {
		t.Fatal(err)
	}
	want := `{"database":"shop","table":"users","column":"mail","rule":"pii-email","rows":2,"scanned":3}` + "\n" +
		`{"database":"shop","table":"users","column":"memo","rule":"pii-email","rows":1,"scanned":40}` + "\n"
	if b.String() != want {
		t.Errorf("json:\n%s\nwant:\n%s", b.String(), want)
	}
	var empty bytes.Buffer
	if err := WriteJSON(&empty, nil); err != nil || empty.Len() != 0 {
		t.Errorf("no findings: %q, %v", empty.String(), err)
	}
}

func TestOutputNeverHoldsTheValue(t *testing.T) {
	const secret = "alice.secret@example.com"
	f := &fakeReader{
		tables: map[string][]source.TableInfo{"shop": {{Name: "users", Columns: cols("mail", "VARCHAR")}}},
		rows:   map[string][][]sql.RawBytes{"shop.users": {row(secret)}},
	}
	got, err := Run(context.Background(), f, piiConfig(t), []string{"shop"})
	if err != nil || len(got) != 1 {
		t.Fatalf("findings = %+v, %v", got, err)
	}
	var b bytes.Buffer
	_ = WriteTable(&b, got)
	_ = WriteJSON(&b, got)
	if strings.Contains(b.String(), "alice") || strings.Contains(b.String(), "example.com") {
		t.Errorf("output holds the value: %s", b.String())
	}
}
