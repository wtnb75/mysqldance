package dump

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
	"mysqldance/internal/writer"
)

// fakeRows is a row source of n rows (id, 'row <id>') that fails with err
// instead of the row failAt (0 = never), like a read that dies mid-stream.
type fakeRows struct {
	n, failAt int
	err       error
	each      func(i int) // called before each row
}

func (f fakeRows) EachRow(_ context.Context, _ source.TableInfo, fn func([]sqlvalue.Value) error) error {
	for i := range f.n {
		if f.each != nil {
			f.each(i)
		}
		if f.failAt != 0 && i == f.failAt {
			return f.err
		}
		row := []sqlvalue.Value{
			sqlvalue.NewValue("INT", fmt.Append(nil, i), false),
			sqlvalue.NewValue("VARCHAR", fmt.Appendf(nil, "row %d", i), false),
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

var streamTable = source.TableInfo{
	Name:         "t",
	Columns:      []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: "s", TypeName: "VARCHAR"}},
	OrderBy:      []string{"id"},
	OrderByTypes: []string{"int"},
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// A read that fails after some rows were already written must fail the table,
// and every file it staged must be removable: nothing is left behind and no
// final file appears (the stage renames only at commit).
func TestWriteTableReadFailureLeavesNothing(t *testing.T) {
	boom := errors.New("connection lost")
	for _, rowsPerFile := range []uint32{0, 20} {
		t.Run(fmt.Sprint("rowsPerFile=", rowsPerFile), func(t *testing.T) {
			dir := t.TempDir()
			opt := Options{OutDir: dir, Writer: writer.NewMySQL(datafile.Options{RowsPerFile: rowsPerFile})}
			st := &stage{dir: dir}
			_, err := writeTable(context.Background(), opt, fakeRows{n: 500, failAt: 300, err: boom}, "d", streamTable, st)
			if !errors.Is(err, boom) || !strings.Contains(err.Error(), `table "t"`) {
				t.Fatalf("err = %v, want the read error worded with the table", err)
			}
			if len(st.files) == 0 {
				t.Fatal("test needs data staged before the failure")
			}
			if err := st.discard(); err != nil {
				t.Fatal(err)
			}
			if left := dirEntries(t, dir); len(left) != 0 {
				t.Errorf("left behind after discard: %v", left)
			}
		})
	}
}

func TestWriteTableNamesFilesAfterTheirCount(t *testing.T) {
	for _, c := range []struct {
		rowsPerFile uint32
		multi       bool
	}{{0, false}, {50, true}, {100000, false}} {
		dir := t.TempDir()
		opt := Options{OutDir: dir, Writer: writer.NewMySQL(datafile.Options{RowsPerFile: c.rowsPerFile})}
		st := &stage{dir: dir}
		names, err := writeTable(context.Background(), opt, fakeRows{n: 1000}, "d", streamTable, st)
		if err != nil {
			t.Fatal(err)
		}
		if (len(names) > 1) != c.multi {
			t.Fatalf("rowsPerFile=%d: %d files, multi=%v", c.rowsPerFile, len(names), c.multi)
		}
		if want := naming.DataFileParts("d", "t", len(names)); strings.Join(names, " ") != strings.Join(want, " ") {
			t.Fatalf("names = %v, want %v", names, want)
		}
		if err := st.commit(); err != nil {
			t.Fatal(err)
		}
		rows := 0
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				t.Fatal(err)
			}
			rows += strings.Count(string(b), "\n(")
		}
		if rows != 1000 {
			t.Errorf("rowsPerFile=%d: %d rows written, want 1000", c.rowsPerFile, rows)
		}
		if left := dirEntries(t, dir); len(left) != len(names) {
			t.Errorf("rowsPerFile=%d: directory = %v, want only %v", c.rowsPerFile, left, names)
		}
	}
}

// With a key, memory must not grow with the number of rows: one million rows
// go through the writer and the staged file while the live heap stays within a
// fixed bound (holding them would take well over 100 MB).
func TestWriteTableMemoryDoesNotGrowWithRows(t *testing.T) {
	if testing.Short() {
		t.Skip("one million rows")
	}
	const rows = 1_000_000
	const bound = 16 << 20
	dir := t.TempDir()
	opt := Options{OutDir: dir, Writer: writer.NewMySQL(datafile.Options{})}
	st := &stage{dir: dir}

	var base, peak uint64
	sample := func(i int) {
		if i%100000 != 0 {
			return
		}
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if i == 0 {
			base = m.HeapAlloc
		}
		peak = max(peak, m.HeapAlloc)
	}
	if _, err := writeTable(context.Background(), opt, fakeRows{n: rows, each: sample}, "d", streamTable, st); err != nil {
		t.Fatal(err)
	}
	if err := st.discard(); err != nil {
		t.Fatal(err)
	}
	t.Logf("live heap: %d bytes at the start, peak %d bytes over %d rows", base, peak, rows)
	if peak > base+bound {
		t.Errorf("live heap grew by %d bytes over %d rows, want at most %d", peak-base, rows, bound)
	}
}

// TestBigKeyedTableStreams dumps a real table of MYSQLDANCE_TEST_BIGROWS rows
// (for example 1000000; it is skipped when unset, as the table takes a while to
// build and write) and checks that the live heap stays far below the size of
// the rows, i.e. that memory does not grow with the table. Run it with
//
//	MYSQLDANCE_TEST_BIGROWS=1000000 task test:integration TESTFLAGS='-run TestBigKeyedTableStreams -v'
func TestBigKeyedTableStreams(t *testing.T) {
	rows, _ := strconv.Atoi(os.Getenv("MYSQLDANCE_TEST_BIGROWS"))
	if rows <= 0 {
		t.Skip("MYSQLDANCE_TEST_BIGROWS is not set")
	}
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	q := sqlvalue.QuoteIdent(db) + ".`big`"
	e.exec(t,
		"CREATE TABLE "+q+" (id INT PRIMARY KEY, s VARCHAR(100), b BLOB)",
		fmt.Sprintf("INSERT /*+ SET_VAR(cte_max_recursion_depth=%d) */ INTO %s "+
			"WITH RECURSIVE n (i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < %d) "+
			"SELECT i, CONCAT('row ', i, REPEAT('x', 40)), REPEAT(CHAR(i MOD 251), 100) FROM n", rows+10, q, rows),
	)

	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base, peak := m.HeapAlloc, m.HeapAlloc
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(250 * time.Millisecond):
				runtime.GC()
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	})
	dir := t.TempDir()
	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir})
	close(done)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	// The rows are about 200 bytes each as text, 400 and more as values.
	bound := uint64(32 << 20)
	t.Logf("%d rows: live heap %d bytes at the start, peak %d bytes (bound %d above the start)", rows, base, peak, bound)
	if peak > base+bound {
		t.Errorf("live heap grew by %d bytes over %d rows, want at most %d", peak-base, rows, bound)
	}
}
