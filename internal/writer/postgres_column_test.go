package writer

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/sqlvalue"
)

// renameColumn renames a column of a table in the schema's table list and
// definition, and in every index and foreign key that names it.
func renameColumn(s *Schema, table, from, to string) {
	for i := range s.Tables {
		if s.Tables[i].Name != table {
			continue
		}
		for j := range s.Tables[i].Columns {
			if s.Tables[i].Columns[j].Name == from {
				s.Tables[i].Columns[j].Name = to
			}
		}
		for j := range s.Tables[i].OrderBy {
			if s.Tables[i].OrderBy[j] == from {
				s.Tables[i].OrderBy[j] = to
			}
		}
	}
	for name, d := range s.Defs {
		if name == table {
			for i := range d.Columns {
				if d.Columns[i].Name == from {
					d.Columns[i].Name = to
				}
			}
			for i := range d.Indexes {
				for j := range d.Indexes[i].Parts {
					if d.Indexes[i].Parts[j].Column == from {
						d.Indexes[i].Parts[j].Column = to
					}
				}
			}
		}
		for i := range d.ForeignKeys {
			fk := &d.ForeignKeys[i]
			if name == table {
				for j := range fk.Columns {
					if fk.Columns[j] == from {
						fk.Columns[j] = to
					}
				}
			}
			if fk.RefSchema == s.DB && fk.RefTable == table {
				for j := range fk.RefColumns {
					if fk.RefColumns[j] == from {
						fk.RefColumns[j] = to
					}
				}
			}
		}
	}
}

// A column name over the limit is shortened in the SQL: CREATE TABLE, the
// indexes and the foreign key (both its own columns and the referenced ones) all
// use the same name, and each shortened column is reported once.
func TestPostgresShortensLongColumnName(t *testing.T) {
	long := strings.Repeat("c", 64)
	short := pgShortName(long)
	if short == long || len(short) > pgMaxIdent {
		t.Fatalf("short = %q", short)
	}
	q := sqlvalue.QuotePGIdent(short)
	s := pgSchema()
	renameColumn(&s, "parent", "code", long)
	renameColumn(&s, "child", "code", long)
	got, warns := writeSchemaText(t, s)
	for _, want := range []string{
		"  " + q + " varchar(10) NOT NULL,\n",
		"  " + q + " varchar(10) NOT NULL DEFAULT 'x',\n",
		"ON \"parent\" (" + q + ");",
		"ON \"parent\" (\"id\", " + q + ");",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, long) {
		t.Errorf("the full name is still in the output:\n%s", got)
	}
	var n int
	for _, w := range warns {
		if strings.Contains(w, "column `"+long+"` is named `"+short+"` in PostgreSQL") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("want one warning per column (2), got %d in %q", n, warns)
	}

	cons, _, err := writeConstraintsText(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if want := `FOREIGN KEY ("parent_id", ` + q + `) REFERENCES "parent" ("id", ` + q + `)`; !strings.Contains(cons, want) || strings.Contains(cons, long) {
		t.Errorf("the foreign key does not use %s:\n%s", short, cons)
	}
}

// A column name within the limit is kept and says nothing.
func TestPostgresKeepsColumnNameWithinLimit(t *testing.T) {
	name := strings.Repeat("c", pgMaxIdent)
	s := pgSchema()
	renameColumn(&s, "parent", "code", name)
	got, warns := writeSchemaText(t, s)
	if !strings.Contains(got, "  "+sqlvalue.QuotePGIdent(name)+" varchar(10) NOT NULL,") {
		t.Errorf("the name was changed:\n%s", got)
	}
	if containsAny(warns, "column `"+name+"` is named") {
		t.Errorf("a name within the limit was reported: %q", warns)
	}
}

// Two long names that start alike stay apart, and the same input gives the same
// bytes.
func TestPostgresLongColumnNamesThatStartAlike(t *testing.T) {
	a, b := strings.Repeat("c", 70)+"1", strings.Repeat("c", 70)+"2"
	build := func() Schema {
		s := pgSchema()
		renameColumn(&s, "child", "code", a)
		renameColumn(&s, "child", "note", b)
		return s
	}
	got, warns := writeSchemaText(t, build())
	for _, n := range []string{a, b} {
		if !strings.Contains(got, sqlvalue.QuotePGIdent(pgShortName(n))+" ") {
			t.Errorf("no column for %s in:\n%s", pgShortName(n), got)
		}
	}
	// Columns are in the order of the table: code before note.
	ia := indexOfWarning(warns, "`"+a+"`")
	ib := indexOfWarning(warns, "`"+b+"`")
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("warnings are not in column order: %q", warns)
	}
	again, _ := writeSchemaText(t, build())
	if again != got {
		t.Error("two renderings of the same schema differ")
	}
}

func indexOfWarning(list []string, sub string) int {
	for i, s := range list {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}

// Two columns of one table that become one name are an error that names both as
// MySQL has them (a failed run prints no warnings). The same name in two tables
// is not a collision.
func TestPostgresShortenedColumnNameCollisionIsAnError(t *testing.T) {
	long := strings.Repeat("c", 64)
	s := pgSchema()
	renameColumn(&s, "child", "code", long)
	renameColumn(&s, "child", "note", pgShortName(long))
	var b bytes.Buffer
	err := NewPostgres(datafile.Options{}).WriteSchema(&b, s)
	if err == nil {
		t.Fatalf("WriteSchema succeeded:\n%s", b.String())
	}
	for _, want := range []string{long, pgShortName(long)} {
		if !strings.Contains(err.Error(), "`"+want+"`") {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}

	ok := pgSchema()
	renameColumn(&ok, "child", "code", long)
	renameColumn(&ok, "parent", "code", long)
	writeSchemaText(t, ok) // fails the test on an error
}

// The INSERT lists a long column by the name CREATE TABLE gave it.
func TestPostgresDataListsShortenedColumnName(t *testing.T) {
	long := strings.Repeat("s", 64)
	info, _ := boolSchema()
	info.ListColumns = true
	info.Columns[3].Name = long
	info.Def.Columns[3].Name = long
	got := renderParts(t, dataParts(NewPostgres(datafile.Options{}), "d", info, [][]sqlvalue.Value{pgRow("1", "1", "\x01", "a", "<null>")}))
	want := `INSERT INTO "t" ("id", "ok", "bits", ` + sqlvalue.QuotePGIdent(pgShortName(long)) + `, "ts") VALUES`
	if len(got) != 1 || !strings.Contains(got[0], want) || strings.Contains(got[0], long) {
		t.Errorf("the data file does not use the short name:\n%q", got)
	}
}
