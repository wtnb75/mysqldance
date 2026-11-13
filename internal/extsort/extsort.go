// Package extsort sorts (key, value) records that do not fit in memory: it
// collects them up to a memory limit, writes each sorted batch (a run) to a
// scratch file, and merges the runs.
package extsort

import (
	"bufio"
	"bytes"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

// File is a scratch file the sorter writes a run to and reads it back from.
// Remove closes and deletes it; it may be called more than once.
type File interface {
	io.ReadWriteSeeker
	Remove() error
}

// NewFile creates a scratch file.
type NewFile func() (File, error)

// maxFanIn is the most runs merged at once, which bounds the number of open
// files and read buffers. More runs are merged in passes. A variable so tests
// can lower it.
var maxFanIn = 64

// recordOverhead is added to the length of every record held in memory: the
// 48-byte record (two slice headers) plus the rounding of the two allocations
// of the copied key and value. It keeps the estimate on the safe side for tiny
// records.
const recordOverhead = 96

// bufSize is the size of the read and write buffers of a run.
const bufSize = 64 << 10

type record struct{ key, val []byte }

func compare(a, b record) int { return bytes.Compare(a.key, b.key) }

// Sorter sorts records by key in byte order. It is for one goroutine.
type Sorter struct {
	memory  int64
	newFile NewFile
	recs    []record
	size    int64
	runs    []File
}

// New returns a Sorter that keeps about memory bytes of records before it
// writes them to a scratch file made by newFile. With a nil newFile it never
// spills and holds everything in memory.
func New(memory int64, newFile NewFile) *Sorter {
	return &Sorter{memory: memory, newFile: newFile}
}

// Add adds a record. key and val are copied.
func (s *Sorter) Add(key, val []byte) error {
	s.recs = append(s.recs, record{slices.Clone(key), slices.Clone(val)})
	s.size += int64(len(key)+len(val)) + recordOverhead
	if s.newFile != nil && s.size >= s.memory {
		return s.spill()
	}
	return nil
}

// spill writes the records held in memory to a new run.
func (s *Sorter) spill() error {
	// Records with equal keys are equal once anything spills (see Each), so the
	// order among them does not matter.
	slices.SortFunc(s.recs, compare)
	f, err := s.newFile()
	if err != nil {
		return err
	}
	// Recorded before anything is written, so Close removes a partial run.
	s.runs = append(s.runs, f)
	w := bufio.NewWriterSize(f, bufSize)
	for _, r := range s.recs {
		if err := writeRecord(w, r.key, r.val); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	s.recs, s.size = nil, 0
	return nil
}

// Each calls fn with every record in key order (records with equal keys come
// in the order they were added when nothing spilled; otherwise in an order that
// depends on the runs, so equal keys must mean equal records). key and val are
// valid only during the call. If fn fails, Each stops and returns its error.
// Scratch files are removed as their runs are consumed; Close removes any left
// by a failure.
func (s *Sorter) Each(fn func(key, val []byte) error) error {
	if len(s.runs) == 0 {
		slices.SortStableFunc(s.recs, compare)
		for _, r := range s.recs {
			if err := fn(r.key, r.val); err != nil {
				return err
			}
		}
		return nil
	}
	if len(s.recs) > 0 {
		if err := s.spill(); err != nil {
			return err
		}
	}
	for len(s.runs) > maxFanIn {
		if err := s.mergePass(); err != nil {
			return err
		}
	}
	runs := s.runs
	s.runs = nil
	return mergeRuns(runs, fn)
}

// mergePass merges the first maxFanIn runs into one new run, which goes last.
// mergeRuns removes the runs of group whether or not the merge succeeds, so on
// a failure they are no longer in s.runs; out is registered first, so Close
// still removes the partial output.
func (s *Sorter) mergePass() error {
	group := s.runs[:maxFanIn:maxFanIn]
	s.runs = s.runs[maxFanIn:]
	out, err := s.newFile()
	if err != nil {
		s.runs = append(group, s.runs...)
		return err
	}
	s.runs = append(s.runs, out)
	w := bufio.NewWriterSize(out, bufSize)
	err = mergeRuns(group, func(k, v []byte) error { return writeRecord(w, k, v) })
	if err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err = out.Seek(0, io.SeekStart)
	return err
}

// Close removes the scratch files that are left and drops the records held.
func (s *Sorter) Close() error {
	var errs []error
	for _, f := range s.runs {
		errs = append(errs, f.Remove())
	}
	s.runs, s.recs, s.size = nil, nil, 0
	return errors.Join(errs...)
}

// cursor reads one run.
type cursor struct {
	r        *bufio.Reader
	idx      int
	key, val []byte
}

// next reads the next record into c.key and c.val; it returns false at the end.
func (c *cursor) next() (bool, error) {
	n, err := binary.ReadUvarint(c.r)
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if c.key, err = readN(c.r, c.key, n); err != nil {
		return false, err
	}
	if n, err = binary.ReadUvarint(c.r); err != nil {
		return false, unexpected(err)
	}
	if c.val, err = readN(c.r, c.val, n); err != nil {
		return false, err
	}
	return true, nil
}

type cursorHeap []*cursor

func (h cursorHeap) Len() int { return len(h) }
func (h cursorHeap) Less(i, j int) bool {
	if c := bytes.Compare(h[i].key, h[j].key); c != 0 {
		return c < 0
	}
	return h[i].idx < h[j].idx
}
func (h cursorHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *cursorHeap) Push(x any)   { *h = append(*h, x.(*cursor)) }
func (h *cursorHeap) Pop() any {
	old := *h
	c := old[len(old)-1]
	*h = old[:len(old)-1]
	return c
}

// mergeRuns merges runs, each already positioned at its start, calling fn with
// the records in order, and removes every run before it returns.
func mergeRuns(runs []File, fn func(key, val []byte) error) (err error) {
	defer func() {
		for _, f := range runs {
			err = errors.Join(err, f.Remove())
		}
	}()
	var h cursorHeap
	for i, f := range runs {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		c := &cursor{r: bufio.NewReaderSize(f, bufSize), idx: i}
		ok, err := c.next()
		if err != nil {
			return err
		}
		if ok {
			h = append(h, c)
		}
	}
	heap.Init(&h)
	for len(h) > 0 {
		c := h[0]
		if err := fn(c.key, c.val); err != nil {
			return err
		}
		ok, err := c.next()
		if err != nil {
			return err
		}
		if ok {
			heap.Fix(&h, 0)
		} else {
			heap.Pop(&h)
		}
	}
	return nil
}

func writeRecord(w *bufio.Writer, key, val []byte) error {
	var b [10]byte
	for _, p := range [][]byte{key, val} {
		n := binary.PutUvarint(b[:], uint64(len(p)))
		if _, err := w.Write(b[:n]); err != nil {
			return err
		}
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readN reads n bytes into buf (reused when large enough).
func readN(r io.Reader, buf []byte, n uint64) ([]byte, error) {
	if n > 1<<32 {
		return nil, fmt.Errorf("extsort: corrupt run: record of %d bytes", n)
	}
	buf = slices.Grow(buf[:0], int(n))[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, unexpected(err)
	}
	return buf, nil
}
