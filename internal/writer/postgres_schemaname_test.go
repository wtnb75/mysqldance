package writer

import (
	"bytes"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
)

// A database named by --pg-schema becomes the schema given: CREATE SCHEMA, the
// search path of every file and the qualified names in the foreign keys all use
// it, and the MySQL name appears nowhere in the SQL.
func TestPostgresSchemaNameOverride(t *testing.T) {
	p := NewPostgres(datafile.Options{}).WithSchemaNames(map[string]string{"pg_app": "app", "b": "bee"})

	var b bytes.Buffer
	s := crossDBSchema("pg_app", "b")
	if err := p.WriteSchema(&b, s); err != nil {
		t.Fatal(err)
	}
	if want := wantSettings + "CREATE SCHEMA IF NOT EXISTS \"app\";\nSET search_path = \"app\";\n"; !strings.HasPrefix(b.String(), want) {
		t.Errorf("schema file starts\n%s\nwant\n%s", b.String(), want)
	}
	if strings.Contains(b.String(), "pg_app") {
		t.Errorf("the MySQL name is in the schema file:\n%s", b.String())
	}

	info := s.Tables[1]
	def := s.Defs["t"]
	info.Def = &def
	data := renderParts(t, dataParts(p, "pg_app", info, nil))
	if len(data) == 0 || !strings.Contains(data[0], "SET search_path = \"app\";\n") || strings.Contains(data[0], "pg_app") {
		t.Errorf("data file = %q", data)
	}

	both := withPeers(s, crossDBSchema("b", "pg_app"))
	for _, c := range []struct {
		schema Schema
		want   string
	}{
		// The file names stay those of the MySQL databases, so b sorts first and
		// the constraints file of pg_app holds both keys, b's first.
		{both[0], "SET search_path = \"app\";\n" +
			"ALTER TABLE \"bee\".\"c\" ADD CONSTRAINT \"fk_b_pg_app\" FOREIGN KEY (\"r\") REFERENCES \"t\" (\"id\") ON DELETE NO ACTION ON UPDATE NO ACTION;\n" +
			"ALTER TABLE \"c\" ADD CONSTRAINT \"fk_pg_app_b\" FOREIGN KEY (\"r\") REFERENCES \"bee\".\"t\" (\"id\") ON DELETE NO ACTION ON UPDATE NO ACTION;\n"},
		{both[1], "SET search_path = \"bee\";\n"},
	} {
		var out bytes.Buffer
		if err := p.WriteConstraints(&out, c.schema); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != wantSettings+c.want {
			t.Errorf("constraints of %s\n got:\n%s\nwant:\n%s", c.schema.DB, got, wantSettings+c.want)
		}
	}
}

// The 63-byte limit is on the schema name, so a database name over it (MySQL
// allows 64 characters) is fine once it is renamed.
func TestPostgresLongDatabaseNameRenamed(t *testing.T) {
	long := strings.Repeat("d", 64)
	var b bytes.Buffer
	p := NewPostgres(datafile.Options{}).WithSchemaNames(map[string]string{long: "short"})
	if err := p.WriteSchema(&b, crossDBSchema(long, "")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `CREATE SCHEMA IF NOT EXISTS "short";`) {
		t.Errorf("schema file:\n%s", b.String())
	}
}

// Without an entry the name is the database's own, and the refusal of a pg_
// name points at --pg-schema.
func TestPostgresReservedSchemaNameSuggestsFlag(t *testing.T) {
	var b bytes.Buffer
	err := NewPostgres(datafile.Options{}).WriteSchema(&b, crossDBSchema("pg_x", ""))
	if err == nil || !strings.Contains(err.Error(), "--pg-schema") {
		t.Errorf("err = %v, want one that names --pg-schema", err)
	}
}

func TestParsePGSchemas(t *testing.T) {
	long := strings.Repeat("x", 64)
	dbs := []string{"pg_a", "pg_b", "plain"}
	ok, err := ParsePGSchemas([]string{"pg_a=a", "pg_b=b2"}, dbs)
	if err != nil || len(ok) != 2 || ok["pg_a"] != "a" || ok["pg_b"] != "b2" {
		t.Fatalf("ParsePGSchemas = %v, %v", ok, err)
	}
	if m, err := ParsePGSchemas(nil, dbs); err != nil || m != nil {
		t.Errorf("no specs = %v, %v, want nil", m, err)
	}
	bad := map[string][]string{
		"no equal sign":      {"pg_a"},
		"empty db":           {"=a"},
		"empty schema":       {"pg_a="},
		"not dumped":         {"other=o"},
		"given twice":        {"pg_a=a", "pg_a=b"},
		"schema twice":       {"pg_a=s", "pg_b=s"},
		"pg_ target":         {"pg_a=pg_x"},
		"information_schema": {"pg_a=information_schema"},
		"over 63 bytes":      {"pg_a=" + long},
		"clash with a db":    {"pg_a=plain"},
		"clash with new one": {"pg_a=pg_b"},
	}
	for name, specs := range bad {
		t.Run(name, func(t *testing.T) {
			if m, err := ParsePGSchemas(specs, dbs); err == nil {
				t.Errorf("accepted %q: %v", specs, m)
			}
		})
	}
	// A database named twice does not clash with itself.
	if _, err := ParsePGSchemas([]string{"pg_a=a"}, []string{"pg_a", "plain", "plain"}); err != nil {
		t.Errorf("a repeated database was refused: %v", err)
	}
	// A schema name taken from a database that is renamed away is free.
	if _, err := ParsePGSchemas([]string{"plain=other", "pg_a=plain"}, dbs); err != nil {
		t.Errorf("a name freed by another entry was refused: %v", err)
	}
}
