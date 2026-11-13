package extsort

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
)

// diskFile is a scratch file in a temporary directory.
type diskFile struct{ *os.File }

func (f diskFile) Remove() error {
	_ = f.Close()
	if err := os.Remove(f.Name()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// TestSortMemoryIsBounded sorts a million records of about 60 bytes (60 MB, so
// far more than the 4 MiB limit) through real files and checks that the live
// heap does not grow with the number of records, and that the output is in
// order and complete.
func TestSortMemoryIsBounded(t *testing.T) {
	const n = 1_000_000
	const limit = 4 << 20
	dir := t.TempDir()
	s := New(limit, func() (File, error) {
		f, err := os.CreateTemp(dir, "run-*")
		return diskFile{f}, err
	})
	defer func() { _ = s.Close() }()

	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base, peak := m.HeapAlloc, m.HeapAlloc
	sample := func() {
		runtime.GC()
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapAlloc)
	}
	// A multiplicative walk modulo a prime visits every number below it once, in
	// a scrambled order.
	const prime = 1_000_003
	x := 1
	for i := range n {
		x = x * 7 % prime
		key := fmt.Sprintf("%08d,'row %08d'", x, x)
		if err := s.Add([]byte(key), []byte("value of "+key)); err != nil {
			t.Fatal(err)
		}
		if i%100_000 == 0 {
			sample()
		}
	}
	count, last := 0, []byte(nil)
	err := s.Each(func(k, v []byte) error {
		if bytes.Compare(last, k) > 0 {
			return fmt.Errorf("record %d out of order: %q after %q", count, k, last)
		}
		last = append(last[:0], k...)
		if count++; count%100_000 == 0 {
			sample()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("got %d records, want %d", count, n)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%d scratch files left after Each", len(entries))
	}
	// The limit plus the read buffers of the merge, with room to spare. Holding
	// every record would take well over 100 MB.
	bound := uint64(48 << 20)
	t.Logf("%d records: live heap %d bytes at the start, peak %d bytes (limit %d)", n, base, peak, limit)
	if peak > base+bound {
		t.Errorf("live heap grew by %d bytes over %d records, want at most %d", peak-base, n, bound)
	}
}
