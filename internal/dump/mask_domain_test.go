package dump

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/mask"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
	"mysqldance/internal/writer"
)

// valueRows is a row source of fixed (id, code) rows that applies the mask of the
// table, as source.Snapshot.EachRow does.
type valueRows struct{ codes []string }

func (v valueRows) EachRow(_ context.Context, t source.TableInfo, fn func([]sqlvalue.Value) error) error {
	for i, c := range v.codes {
		row := []sqlvalue.Value{
			sqlvalue.NewValue("INT", []byte{byte('1' + i)}, false),
			sqlvalue.NewValue("VARCHAR", []byte(c), false),
		}
		if t.Mask != nil {
			if err := t.Mask(row); err != nil {
				return err
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

func domainCatalog() mask.Catalog {
	table := func(name, codeCol string, fks []mask.CatalogFK, indexes []mask.CatalogIndex) mask.CatalogTable {
		return mask.CatalogTable{
			Name: name,
			Columns: []mask.CatalogColumn{
				{Name: "id", TypeName: "INT", ColumnType: "int", DataType: "int"},
				{Name: codeCol, TypeName: "VARCHAR", ColumnType: "varchar(40)", DataType: "varchar", CharacterSet: "utf8mb4"},
			},
			OrderBy:     []string{"id"},
			Indexes:     append([]mask.CatalogIndex{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}}, indexes...),
			ForeignKeys: fks,
		}
	}
	return mask.Catalog{Databases: map[string][]mask.CatalogTable{"d": {
		table("parent", "code", nil, []mask.CatalogIndex{{Name: "uq_code", Unique: true, Columns: []string{"code"}}}),
		table("child", "pcode", []mask.CatalogFK{{Name: "fk", Columns: []string{"pcode"}, RefDB: "d", RefTable: "parent", RefColumns: []string{"code"}}}, nil),
	}}}
}

func domainTableInfo(name, codeCol string, plan *mask.Plan) (source.TableInfo, error) {
	cols := []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: codeCol, TypeName: "VARCHAR"}}
	fn, err := plan.Table("d", name, cols)
	if err != nil {
		return source.TableInfo{}, err
	}
	def := &source.TableDef{Columns: []source.ColumnDef{
		{Name: "id", Type: "int", DataType: "int"},
		{Name: codeCol, Type: "varchar(40)", DataType: "varchar", CharacterSet: "utf8mb4", Collation: "utf8mb4_bin"},
	}}
	return source.TableInfo{Name: name, Columns: cols, OrderBy: []string{"id"}, OrderByTypes: []string{"int"}, Mask: fn, Def: def}, nil
}

// A child column and the parent column it references, given one domain, hold the
// same masked value in the files of every target, and the files are the same
// bytes on every run, one table at a time or both at once.
func TestMaskDomainFollowsForeignKeysInEveryTarget(t *testing.T) {
	reg := mask.DefaultRegistry()
	rules, err := mask.Parse([]byte("version: 1\nrules:\n"+
		"  d.parent.code:\n    use: hash_keep_shape\n    domain: code\n"+
		"  d.child.pcode:\n    use: hash_keep_shape\n    domain: code\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := mask.LoadSalt("", func(string) string { return "0123456789abcdef-test-salt" })
	if err != nil {
		t.Fatal(err)
	}
	plan, err := rules.Resolve(domainCatalog(), reg, &salt)
	if err != nil {
		t.Fatal(err)
	}
	parentInfo, err := domainTableInfo("parent", "code", plan)
	if err != nil {
		t.Fatal(err)
	}
	childInfo, err := domainTableInfo("child", "pcode", plan)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "ALPHA-1234"
	writers := map[string]writer.Writer{
		"mysql":    writer.NewMySQL(datafile.Options{}),
		"sqlite":   writer.NewSQLite(datafile.Options{}),
		"postgres": writer.NewPostgres(datafile.Options{}),
	}
	for name, w := range writers {
		t.Run(name, func(t *testing.T) {
			// run writes both tables, in two goroutines when parallel is set.
			run := func(parallel bool) map[string]string {
				dir := t.TempDir()
				opt := Options{OutDir: dir, Log: testLogger(io.Discard), Writer: w, maskPlan: plan}
				st := &stage{dir: dir}
				names := naming.NewPlan("d", []string{"parent", "child"})
				var wg sync.WaitGroup
				var mu sync.Mutex
				errs := map[string]error{}
				do := func(info source.TableInfo) {
					defer wg.Done()
					_, err := writeTable(context.Background(), opt, valueRows{codes: []string{secret, "BETA-99"}}, names, "d", info, st)
					mu.Lock()
					errs[info.Name] = err
					mu.Unlock()
				}
				wg.Add(2)
				if parallel {
					go do(parentInfo)
					go do(childInfo)
				} else {
					do(parentInfo)
					do(childInfo)
				}
				wg.Wait()
				for table, err := range errs {
					if err != nil {
						t.Fatalf("%s: %v", table, err)
					}
				}
				if err := st.commit(); err != nil {
					t.Fatal(err)
				}
				return readFiles(t, dir)
			}
			first := run(false)
			for _, parallel := range []bool{false, true} {
				again := run(parallel)
				if len(again) != len(first) {
					t.Fatalf("files differ: %d then %d", len(first), len(again))
				}
				for f, b := range first {
					if again[f] != b {
						t.Errorf("parallel=%v: %s differs between runs", parallel, f)
					}
				}
			}
			var parent, child string
			for f, b := range first {
				switch {
				case strings.Contains(f, "parent"):
					parent = b
				case strings.Contains(f, "child"):
					child = b
				}
				if strings.Contains(b, "ALPHA") || strings.Contains(b, "BETA") {
					t.Errorf("%s holds a value that was not masked:\n%s", f, b)
				}
			}
			// The masked values of the parent are the ones the child holds.
			masked := maskedValues(t, plan, parentInfo, secret, "BETA-99")
			for _, v := range masked {
				if !strings.Contains(parent, v) || !strings.Contains(child, v) {
					t.Errorf("the masked value %q is not in both files\nparent:\n%s\nchild:\n%s", v, parent, child)
				}
			}
		})
	}
}

// maskedValues applies the mask of a table to the values of its second column.
func maskedValues(t *testing.T, plan *mask.Plan, info source.TableInfo, values ...string) []string {
	t.Helper()
	var out []string
	for _, v := range values {
		row := []sqlvalue.Value{sqlvalue.NewValue("INT", []byte("1"), false), sqlvalue.NewValue("VARCHAR", []byte(v), false)}
		if err := info.Mask(row); err != nil {
			t.Fatal(err)
		}
		out = append(out, string(row[1].Bytes()))
	}
	return out
}
