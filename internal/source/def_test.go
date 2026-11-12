package source

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestDefs(t *testing.T) {
	cfg, db, conn := testDB(t)
	// MariaDB and MySQL 5.7 (and 5.6; mysql57 below covers both) have no
	// functional key parts. 5.7 also drops DESC of a key part and parses a CHECK
	// without keeping it.
	functionalKey, wantFunctional := " KEY ((qty + 1)),", 1
	mariadb, mysql57 := isMariaDB(t, conn), isMySQL5(t, conn)
	// MySQL 5.6 has no generated columns: twice is a plain column there.
	twiceDef, wantGenerated := "INT AS (parent_id * 2) VIRTUAL", true
	if isMySQL56(t, conn) {
		twiceDef, wantGenerated = "INT", false
	}
	if mariadb || mysql57 {
		functionalKey, wantFunctional = "", 0
	}
	mustExec(t, conn,
		"CREATE TABLE parent (id BIGINT UNSIGNED NOT NULL PRIMARY KEY, code VARCHAR(10) NOT NULL, UNIQUE KEY uk_code (code), UNIQUE KEY uk_id_code (id, code))",
		"CREATE TABLE child ("+
			" id INT NOT NULL AUTO_INCREMENT,"+
			" parent_id BIGINT UNSIGNED NULL,"+
			" code VARCHAR(10) NOT NULL DEFAULT 'x',"+
			" qty DECIMAL(10,2) NOT NULL DEFAULT 1.50,"+
			" created DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,"+
			" note VARCHAR(100) NULL,"+
			" twice "+twiceDef+","+
			" PRIMARY KEY (id),"+
			" KEY idx_note (note(10), created DESC),"+
			functionalKey+
			" FULLTEXT KEY ft_note (note),"+
			" CONSTRAINT fk_parent FOREIGN KEY (parent_id, code) REFERENCES parent (id, code) ON DELETE CASCADE ON UPDATE RESTRICT,"+
			" CONSTRAINT ck_qty CHECK (qty >= 0))",
	)
	s := openSnapshot(t, cfg, db)
	defs, err := s.Defs(context.Background())
	if err != nil {
		t.Fatalf("Defs: %v", err)
	}

	child, ok := defs["child"]
	if !ok {
		t.Fatalf("no definition of child in %v", defs)
	}
	cols := make([]string, len(child.Columns))
	for i, c := range child.Columns {
		cols[i] = c.Name
	}
	if want := []string{"id", "parent_id", "code", "qty", "created", "note", "twice"}; !reflect.DeepEqual(cols, want) {
		t.Errorf("columns = %v, want %v", cols, want)
	}
	byName := map[string]ColumnDef{}
	for _, c := range child.Columns {
		byName[c.Name] = c
	}
	// MariaDB (and MySQL before 8.0.19) report the display width of integers.
	wantType := "bigint unsigned"
	if mariadb || mysql57 {
		wantType = "bigint(20) unsigned"
	}
	if c := byName["parent_id"]; c.Type != wantType || c.DataType != "bigint" || !c.Nullable || c.Default != nil {
		t.Errorf("parent_id = %+v", c)
	}
	if c := byName["code"]; c.Default == nil || *c.Default != "x" || c.Nullable {
		t.Errorf("code = %+v", c)
	}
	if c := byName["qty"]; c.Default == nil || *c.Default != "1.50" || c.Type != "decimal(10,2)" {
		t.Errorf("qty = %+v", c)
	}
	// MariaDB writes the function as current_timestamp().
	if c := byName["created"]; c.Default == nil || !c.DefaultExpr || !c.OnUpdate ||
		strings.ToUpper(strings.TrimSuffix(*c.Default, "()")) != "CURRENT_TIMESTAMP" {
		t.Errorf("created = %+v", c)
	}
	if c := byName["id"]; !c.AutoIncrement {
		t.Errorf("id = %+v", c)
	}
	if c := byName["twice"]; c.Generated != wantGenerated {
		t.Errorf("twice = %+v", c)
	}

	idx := map[string]IndexDef{}
	for _, ix := range child.Indexes {
		idx[ix.Name] = ix
	}
	if ix := idx["PRIMARY"]; !ix.Unique || len(ix.Parts) != 1 || ix.Parts[0].Column != "id" {
		t.Errorf("PRIMARY = %+v", ix)
	}
	ix := idx["idx_note"]
	if ix.Unique || len(ix.Parts) != 2 || ix.Parts[0].Column != "note" || !ix.Parts[0].Prefix ||
		ix.Parts[1].Column != "created" || ix.Parts[1].Desc == mysql57 {
		t.Errorf("idx_note = %+v", ix)
	}
	if ix := idx["ft_note"]; ix.Kind != "FULLTEXT" {
		t.Errorf("ft_note = %+v", ix)
	}
	functional := 0
	for _, ix := range child.Indexes {
		for _, p := range ix.Parts {
			if p.Column == "" {
				functional++
			}
		}
	}
	if functional != wantFunctional {
		t.Errorf("functional key parts = %d, want %d; indexes %+v", functional, wantFunctional, child.Indexes)
	}

	if len(child.ForeignKeys) != 1 {
		t.Fatalf("foreign keys = %+v", child.ForeignKeys)
	}
	fk := child.ForeignKeys[0]
	if fk.Name != "fk_parent" || fk.RefSchema != db || fk.RefTable != "parent" ||
		!reflect.DeepEqual(fk.Columns, []string{"parent_id", "code"}) ||
		!reflect.DeepEqual(fk.RefColumns, []string{"id", "code"}) ||
		fk.OnDelete != "CASCADE" || fk.OnUpdate != "RESTRICT" {
		t.Errorf("fk = %+v", fk)
	}
	wantChecks := []string{"ck_qty"}
	if mysql57 {
		wantChecks = nil
	}
	if !reflect.DeepEqual(child.Checks, wantChecks) {
		t.Errorf("checks = %v, want %v", child.Checks, wantChecks)
	}
	if got := defs["parent"].Indexes; len(got) != 3 {
		t.Errorf("parent indexes = %+v", got)
	}
}

func TestDefsSkipViews(t *testing.T) {
	cfg, db, conn := testDB(t)
	mustExec(t, conn, "CREATE TABLE t (id INT PRIMARY KEY)", "CREATE VIEW v AS SELECT id FROM t")
	defs, err := openSnapshot(t, cfg, db).Defs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defs["v"]; ok {
		t.Errorf("a view has a definition: %v", defs)
	}
	if _, ok := defs["t"]; !ok {
		t.Errorf("no definition of t: %v", defs)
	}
}
