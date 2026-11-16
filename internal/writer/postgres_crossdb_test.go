package writer

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// crossDBSchema is database db with a table t (primary key id) and a table c
// whose column r references table t of database ref (when ref is not empty).
func crossDBSchema(db, ref string) Schema {
	t := source.TableInfo{Name: "t", Columns: []sqlvalue.Column{{Name: "id", TypeName: "INT"}}}
	c := source.TableInfo{Name: "c", Columns: []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: "r", TypeName: "INT"}}}
	pk := source.IndexDef{Name: "PRIMARY", Unique: true, Kind: "BTREE", Parts: []source.IndexPart{{Column: "id"}}}
	cdef := source.TableDef{
		Columns: []source.ColumnDef{{Name: "id", Type: "int", DataType: "int"}, {Name: "r", Type: "int", DataType: "int", Nullable: true}},
		Indexes: []source.IndexDef{pk},
	}
	if ref != "" {
		cdef.ForeignKeys = []source.ForeignKey{{Name: "fk_" + db + "_" + ref, Columns: []string{"r"}, RefSchema: ref, RefTable: "t",
			RefColumns: []string{"id"}, OnUpdate: "NO ACTION", OnDelete: "NO ACTION"}}
	}
	return Schema{
		DB:              db,
		ConstraintsFile: db + ".90-constraints.sql",
		Tables:          []source.TableInfo{c, t},
		Defs: map[string]source.TableDef{
			"c": cdef,
			"t": {Columns: []source.ColumnDef{{Name: "id", Type: "int", DataType: "int"}}, Indexes: []source.IndexDef{pk}},
		},
	}
}

// withPeers gives each schema the others as its peers, in the order given.
func withPeers(all ...Schema) []Schema {
	out := make([]Schema, len(all))
	for i, s := range all {
		s.Peers = nil
		for j, o := range all {
			if j != i {
				s.Peers = append(s.Peers, o)
			}
		}
		out[i] = s
	}
	return out
}

func constraintsText(t *testing.T, s Schema) (string, []string) {
	t.Helper()
	text, warns, err := writeConstraintsText(t, s)
	if err != nil {
		t.Fatal(err)
	}
	return text, warns
}

const (
	fkAtoB = `ALTER TABLE "a"."c" ADD CONSTRAINT "fk_a_b" FOREIGN KEY ("r") REFERENCES "t" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION;` + "\n"
	fkBtoA = `ALTER TABLE "c" ADD CONSTRAINT "fk_b_a" FOREIGN KEY ("r") REFERENCES "a"."t" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION;` + "\n"
)

// A foreign key between two databases goes to the constraints file of the one
// that sorts later: that file runs after the schema and data files of both.
func TestPostgresCrossDatabaseForeignKeysGoToTheLaterFile(t *testing.T) {
	both := withPeers(crossDBSchema("a", "b"), crossDBSchema("b", "a"))
	gotA, warnsA := constraintsText(t, both[0])
	gotB, warnsB := constraintsText(t, both[1])
	if want := wantSettings + "SET search_path = \"a\";\n"; gotA != want {
		t.Errorf("a.90-constraints.sql = %q, want only the header %q", gotA, want)
	}
	// In file name order of the owning database, a's key first.
	if want := wantSettings + "SET search_path = \"b\";\n" + fkAtoB + fkBtoA; gotB != want {
		t.Errorf("b.90-constraints.sql differs\n got:\n%s\nwant:\n%s", gotB, want)
	}
	if len(warnsA)+len(warnsB) != 0 {
		t.Errorf("warnings = %q %q, want none", warnsA, warnsB)
	}
}

// Which file a key goes to follows the file names, not the order the databases
// are listed in.
func TestPostgresCrossDatabaseForeignKeysDoNotDependOnPeerOrder(t *testing.T) {
	a, b, c := crossDBSchema("a", "c"), crossDBSchema("b", "a"), crossDBSchema("c", "b")
	var want [3]string
	for i, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}} {
		all := []Schema{a, b, c}
		perm := make([]Schema, 3)
		for k, j := range order {
			perm[k] = all[j]
		}
		got := withPeers(perm...)
		for k, j := range order {
			text, _ := constraintsText(t, got[k])
			if i == 0 {
				want[j] = text
			} else if text != want[j] {
				t.Errorf("order %v: file of %s differs\n got:\n%s\nwant:\n%s", order, all[j].DB, text, want[j])
			}
		}
	}
	// a->c and c->b go to c, b->a goes to b, and a holds none.
	for i, wantKeys := range []int{0, 1, 2} {
		if n := strings.Count(want[i], "ADD CONSTRAINT"); n != wantKeys {
			t.Errorf("file of database %d has %d foreign keys, want %d:\n%s", i, n, wantKeys, want[i])
		}
	}
}

// A key into a database that is not dumped is reported and left out, and the
// message says why.
func TestPostgresForeignKeyIntoADatabaseThatIsNotDumped(t *testing.T) {
	s := crossDBSchema("a", "gone")
	got, warns := constraintsText(t, s)
	if strings.Contains(got, "ADD CONSTRAINT") {
		t.Errorf("the foreign key was written:\n%s", got)
	}
	if !containsAny(warns, "foreign key `fk_a_gone` references database `gone`, which is not dumped") {
		t.Errorf("warnings = %q", warns)
	}
}

// A key into a dumped database is checked against that database's definitions,
// and a key that fails is reported once, by the database it belongs to.
func TestPostgresCrossDatabaseForeignKeyIsCheckedAndReportedOnce(t *testing.T) {
	b := crossDBSchema("b", "")
	d := b.Defs["t"]
	d.Indexes = nil // no key behind the referenced column any more
	b.Defs["t"] = d
	both := withPeers(crossDBSchema("a", "b"), b)
	var all []string
	for _, s := range both {
		got, warns := constraintsText(t, s)
		if strings.Contains(got, "ADD CONSTRAINT") {
			t.Errorf("%s: a foreign key without a unique target was written:\n%s", s.DB, got)
		}
		if s.DB == "b" && len(warns) != 0 {
			t.Errorf("b reported a key of a: %q", warns)
		}
		all = append(all, warns...)
	}
	if len(all) != 1 || !strings.Contains(all[0], "not a primary key or unique key") {
		t.Errorf("warnings = %q, want one about the target not being a key", all)
	}
}

// A shortened name is reported by the database of the table, and a clash of
// names within a table is an error there, whichever file the key is in.
func TestPostgresCrossDatabaseForeignKeyNameIsReportedByItsOwner(t *testing.T) {
	long := strings.Repeat("f", 64)
	a := crossDBSchema("a", "b")
	d := a.Defs["c"]
	d.ForeignKeys[0].Name = long
	a.Defs["c"] = d
	both := withPeers(a, crossDBSchema("b", ""))
	var named []string
	for _, s := range both {
		got, warns := constraintsText(t, s)
		for _, w := range warns {
			if strings.Contains(w, "is named") {
				named = append(named, s.DB)
			}
		}
		if s.DB == "b" && !strings.Contains(got, sqlvalue.QuotePGIdent(pgShortName(long))) {
			t.Errorf("b's file lacks the shortened name:\n%s", got)
		}
	}
	if !slices.Equal(named, []string{"a"}) {
		t.Errorf("the shortened name was reported by %v, want only a", named)
	}
}

// Without peers (one database, or no key into another) nothing changes: this is
// the text of a single-database dump.
func TestPostgresConstraintsOfOneDatabaseAreUnchangedByPeers(t *testing.T) {
	alone := crossDBSchema("a", "")
	want, _ := constraintsText(t, alone)
	// A peer that has nothing to do with a changes nothing in its file.
	other := crossDBSchema("z", "")
	got, _ := constraintsText(t, withPeers(alone, other)[0])
	if got != want {
		t.Errorf("a peer changed the file\n got:\n%s\nwant:\n%s", got, want)
	}
	var b bytes.Buffer
	if err := NewPostgres(datafile.Options{}).WriteConstraints(&b, alone); err != nil || b.String() != want {
		t.Errorf("err = %v, text = %q", err, b.String())
	}
}
