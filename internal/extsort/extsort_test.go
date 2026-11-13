package extsort

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"slices"
	"testing"
)

// memFile is an in-memory scratch file that records whether it was removed.
type memFile struct {
	buf     bytes.Buffer
	removed bool
}

func (f *memFile) Read(p []byte) (int, error)  { return f.buf.Read(p) }
func (f *memFile) Write(p []byte) (int, error) { return f.buf.Write(p) }

// Seek only supports rewinding to the start, which is all the sorter needs.
func (f *memFile) Seek(off int64, whence int) (int64, error) {
	if off != 0 || whence != io.SeekStart {
		return 0, errors.New("memFile: only Seek(0, SeekStart)")
	}
	f.buf = *bytes.NewBuffer(slices.Clone(f.buf.Bytes()))
	return 0, nil
}
func (f *memFile) Remove() error { f.removed = true; return nil }

type scratch struct {
	files []*memFile
	fail  error // returned by the next create when set
}

func (s *scratch) create() (File, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	f := &memFile{}
	s.files = append(s.files, f)
	return f, nil
}

func (s *scratch) allRemoved() bool {
	for _, f := range s.files {
		if !f.removed {
			return false
		}
	}
	return true
}

type rec struct{ key, val string }

func collect(t *testing.T, s *Sorter) []rec {
	t.Helper()
	var out []rec
	err := s.Each(func(k, v []byte) error {
		out = append(out, rec{string(k), string(v)})
		return nil
	})
	if err != nil {
		t.Fatalf("Each: %v", err)
	}
	return out
}

func randomRecs(n int) []rec {
	r := rand.New(rand.NewSource(1))
	out := make([]rec, n)
	for i := range out {
		// Few distinct keys so duplicates occur; the value follows the key so
		// equal keys carry equal values, as they do for rows.
		k := fmt.Sprintf("k%03d", r.Intn(n/3+1))
		out[i] = rec{k, "v-" + k}
	}
	return out
}

func sorted(in []rec) []rec {
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b rec) int { return bytes.Compare([]byte(a.key), []byte(b.key)) })
	return out
}

func TestSortInMemoryWithoutSpill(t *testing.T) {
	in := randomRecs(500)
	s := New(1, nil) // a tiny limit, but no way to spill: everything stays in memory
	for _, r := range in {
		if err := s.Add([]byte(r.key), []byte(r.val)); err != nil {
			t.Fatal(err)
		}
	}
	if got := collect(t, s); !slices.Equal(got, sorted(in)) {
		t.Fatalf("not sorted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSortSpillsAndMatchesInMemory(t *testing.T) {
	in := randomRecs(2000)
	sc := &scratch{}
	s := New(4096, sc.create)
	for _, r := range in {
		if err := s.Add([]byte(r.key), []byte(r.val)); err != nil {
			t.Fatal(err)
		}
	}
	got := collect(t, s)
	if !slices.Equal(got, sorted(in)) {
		t.Fatalf("spilled sort differs from in-memory sort")
	}
	if len(sc.files) < 2 {
		t.Fatalf("expected several runs, got %d scratch files", len(sc.files))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !sc.allRemoved() {
		t.Fatalf("scratch files left after Close")
	}
}

func TestSortMergesInPassesWhenManyRuns(t *testing.T) {
	old := maxFanIn
	maxFanIn = 3
	t.Cleanup(func() { maxFanIn = old })
	in := randomRecs(1000)
	sc := &scratch{}
	s := New(512, sc.create)
	for _, r := range in {
		if err := s.Add([]byte(r.key), []byte(r.val)); err != nil {
			t.Fatal(err)
		}
	}
	if got := collect(t, s); !slices.Equal(got, sorted(in)) {
		t.Fatalf("multi-pass merge differs from in-memory sort")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !sc.allRemoved() {
		t.Fatalf("scratch files left after Close")
	}
}

func TestSortRemovesRunsAsTheyAreMerged(t *testing.T) {
	sc := &scratch{}
	s := New(256, sc.create)
	for _, r := range randomRecs(500) {
		if err := s.Add([]byte(r.key), []byte(r.val)); err != nil {
			t.Fatal(err)
		}
	}
	collect(t, s)
	// Each has returned, so the runs are consumed and gone even before Close.
	if !sc.allRemoved() {
		t.Fatalf("scratch files left after Each")
	}
	_ = s.Close()
}

func TestSortEmpty(t *testing.T) {
	sc := &scratch{}
	s := New(1024, sc.create)
	if got := collect(t, s); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if len(sc.files) != 0 {
		t.Fatalf("empty sort made scratch files")
	}
}

func TestSortBinarySafeKeysAndValues(t *testing.T) {
	sc := &scratch{}
	s := New(1, sc.create)
	in := []rec{
		{"b\x00\xff", "\x00\n\r"},
		{"a", ""},
		{"", "empty key"},
		{"b\x00", strings300()},
	}
	for _, r := range in {
		if err := s.Add([]byte(r.key), []byte(r.val)); err != nil {
			t.Fatal(err)
		}
	}
	if got := collect(t, s); !slices.Equal(got, sorted(in)) {
		t.Fatalf("got %q", got)
	}
	_ = s.Close()
}

func strings300() string { return string(bytes.Repeat([]byte{'x'}, 300)) }

func TestSortFnErrorStopsAndCloseRemoves(t *testing.T) {
	sc := &scratch{}
	s := New(256, sc.create)
	for _, r := range randomRecs(500) {
		if err := s.Add([]byte(r.key), []byte(r.val)); err != nil {
			t.Fatal(err)
		}
	}
	boom := errors.New("boom")
	n := 0
	err := s.Each(func(_, _ []byte) error {
		n++
		if n == 10 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || n != 10 {
		t.Fatalf("err = %v after %d rows", err, n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !sc.allRemoved() {
		t.Fatalf("scratch files left after a failed Each and Close")
	}
}

func TestSortCreateErrorSurfaces(t *testing.T) {
	boom := errors.New("disk full")
	sc := &scratch{fail: boom}
	s := New(64, sc.create)
	var err error
	for _, r := range randomRecs(100) {
		if err = s.Add([]byte(r.key), []byte(r.val)); err != nil {
			break
		}
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	_ = s.Close()
}

func TestCloseWithoutEachRemovesRuns(t *testing.T) {
	sc := &scratch{}
	s := New(256, sc.create)
	for _, r := range randomRecs(500) {
		if err := s.Add([]byte(r.key), []byte(r.val)); err != nil {
			t.Fatal(err)
		}
	}
	if len(sc.files) == 0 {
		t.Fatal("expected runs")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !sc.allRemoved() {
		t.Fatalf("scratch files left after Close")
	}
}
