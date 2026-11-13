package dump

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mysqldance/internal/naming"
	"mysqldance/internal/sqlvalue"
)

// keylessTable creates a table without a key in a new database: many rows
// with duplicates, NULLs, an empty string and binary bytes, so that a sort
// limited to a few kilobytes needs many runs.
func (e *env) keylessTable(t *testing.T) string {
	t.Helper()
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE nokey (a INT, b VARCHAR(20), c VARBINARY(8))")
	for i := range 20 {
		var vals []string
		for j := range 150 {
			n := i*150 + j
			switch n % 7 {
			case 0:
				vals = append(vals, fmt.Sprintf("(%d, NULL, NULL)", n%401))
			case 1:
				vals = append(vals, fmt.Sprintf("(%d, '', X'00ff')", n%401))
			default:
				vals = append(vals, fmt.Sprintf("(%d, 'k%d', X'0a27')", n%401, n%53))
			}
		}
		e.exec(t, "INSERT INTO nokey VALUES "+strings.Join(vals, ","))
	}
	return db
}

func TestExternalSortOutputMatchesInMemory(t *testing.T) {
	e := newEnv(t)
	db := e.keylessTable(t)

	// The default limit holds the 3000 rows in memory.
	want := t.TempDir()
	e.dump(t, want, db)

	// A limit of 4 KiB writes dozens of runs to scratch files.
	got := t.TempDir()
	if err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: got, SortMemory: 4 << 10}); err != nil {
		t.Fatal(err)
	}
	if a, b := readFiles(t, want), readFiles(t, got); !reflect.DeepEqual(a, b) {
		t.Errorf("output with the external sort differs from the in-memory sort")
	}
	for name := range readFiles(t, got) {
		if isTempName(name) {
			t.Errorf("temporary file %s left after a successful run", name)
		}
	}
	e.roundTrip(t, got, db)
}

func TestExternalSortFailureLeavesNoScratchFiles(t *testing.T) {
	e := newEnv(t)
	db := e.keylessTable(t)
	dir := t.TempDir()
	e.dump(t, dir, db)
	before := readFiles(t, dir)

	// The write of the data file fails while the sorted runs are being played
	// back, so scratch files are still on disk.
	failOn(t, filepath.Join(dir, naming.DataFile(db, "nokey")), nil)
	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, SortMemory: 4 << 10})
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("Run = %v, want the injected write failure", err)
	}
	if after := readFiles(t, dir); !reflect.DeepEqual(after, before) {
		t.Errorf("directory changed after a failed run (a scratch file left?):\nbefore %v\nafter  %v", keys(before), keys(after))
	}
}

// TestBigKeylessTableSorts dumps a real table without a key of
// MYSQLDANCE_TEST_BIGROWS rows (for example 1000000; skipped when unset) with a
// 16 MiB sort limit, and checks that the live heap stays far below the size of
// the rows and that no scratch file is left. (The order is checked against
// the in-memory sort by TestExternalSortOutputMatchesInMemory.)
// Run it with
//
//	MYSQLDANCE_TEST_BIGROWS=1000000 task test:integration TESTFLAGS='-run TestBigKeylessTableSorts -v'
func TestBigKeylessTableSorts(t *testing.T) {
	rows, _ := strconv.Atoi(os.Getenv("MYSQLDANCE_TEST_BIGROWS"))
	if rows <= 0 {
		t.Skip("MYSQLDANCE_TEST_BIGROWS is not set")
	}
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	q := sqlvalue.QuoteIdent(db) + ".`big`"
	// A scrambled order and duplicates (i MOD 900001), so nothing is sorted
	// already.
	e.exec(t,
		"CREATE TABLE "+q+" (id INT, s VARCHAR(100), b BLOB)",
		fmt.Sprintf("INSERT /*+ SET_VAR(cte_max_recursion_depth=%d) */ INTO %s "+
			"WITH RECURSIVE n (i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < %d) "+
			"SELECT (i * 7919) MOD 900001, CONCAT('row ', i MOD 900001, REPEAT('x', 40)), REPEAT(CHAR((i MOD 900001) MOD 251), 100) FROM n",
			rows+10, q, rows),
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
	err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, SortMemory: 16 << 20})
	close(done)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	// The rows are about 200 bytes each as text, 400 and more as values: holding
	// them would take hundreds of megabytes.
	bound := uint64(96 << 20)
	t.Logf("%d rows: live heap %d bytes at the start, peak %d bytes (bound %d above the start)", rows, base, peak, bound)
	if peak > base+bound {
		t.Errorf("live heap grew by %d bytes over %d rows, want at most %d", peak-base, rows, bound)
	}
	for name := range readFiles(t, dir) {
		if isTempName(name) {
			t.Errorf("temporary file %s left after a successful run", name)
		}
	}
}

func keys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
