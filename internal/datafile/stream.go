package datafile

import (
	"bufio"
	"io"
	"strings"
)

// Stream writes the rows of one table, one at a time and already in output
// order, as the INSERT statements of one or more data files. Only the row being
// added and the write buffer are held, so memory does not grow with the number
// of rows. The bytes are those of Split followed by WriteStatements on each
// group: both decisions (where a statement ends, where a file ends) depend on
// the row alone, never on how many rows follow.
type Stream struct {
	open    func() (io.WriteCloser, error)
	header  string
	footer  string
	head    string // the "INSERT INTO ... VALUES\n" prefix
	keyCols []int
	opt     Options

	f       *fileStream
	nextNew bool // the open file ended with the last row; start a new one on the next
	err     error
}

// NewStream returns a Stream that calls open for each data file it needs. The
// first call happens on the first row, or on Close for a table without rows, so
// that file always exists. header and footer are written at the start and end
// of every file; quote renders identifiers (see WriteStatements). keyCols are
// the indexes of the key columns in a row, used to cut files when
// opt.RowsPerFile is set (see Split). The Stream closes every file it opens.
func NewStream(open func() (io.WriteCloser, error), header, footer string, quote func(string) string,
	table string, columns []string, listColumns bool, keyCols []int, opt Options) *Stream {
	if opt.RowsPerInsert == 0 {
		opt.RowsPerInsert = defaultRowsPerInsert
	}
	if opt.MaxInsertBytes <= 0 {
		opt.MaxInsertBytes = defaultMaxInsertBytes
	}
	var head strings.Builder
	head.WriteString("INSERT INTO " + quote(table))
	if listColumns {
		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = quote(c)
		}
		head.WriteString(" (" + strings.Join(quoted, ", ") + ")")
	}
	head.WriteString(" VALUES\n")
	return &Stream{open: open, header: header, footer: footer, head: head.String(), keyCols: keyCols, opt: opt}
}

// Add writes one row. After an error the Stream is unusable and Close reports
// the same error.
func (s *Stream) Add(row []string) error {
	if s.err != nil {
		return s.err
	}
	if s.nextNew {
		s.err = s.finish()
		if s.err != nil {
			return s.err
		}
	}
	if s.f == nil {
		if s.err = s.begin(); s.err != nil {
			return s.err
		}
	}
	s.err = s.f.add(row)
	if s.err == nil && s.opt.RowsPerFile != 0 && keyHash(row, s.keyCols)%s.opt.RowsPerFile == 0 {
		// A new file starts only if another row arrives: the last row of a
		// table that ends a file must not leave an empty one behind.
		s.nextNew = true
	}
	return s.err
}

// Close ends the last file; a table without rows gets one file with only the
// header and footer. It must be called once, also after an error, to release
// the open file.
func (s *Stream) Close() error {
	if s.err == nil && s.f == nil {
		s.err = s.begin()
	}
	if s.f != nil {
		err := s.finish()
		if s.err == nil {
			s.err = err
		}
	}
	return s.err
}

func (s *Stream) begin() error {
	wc, err := s.open()
	if err != nil {
		return err
	}
	s.f = &fileStream{wc: wc, bw: bufio.NewWriter(wc), head: s.head, opt: s.opt}
	_, err = s.f.bw.WriteString(s.header)
	return err
}

// finish writes the footer and closes the current file.
func (s *Stream) finish() error {
	f := s.f
	s.f, s.nextNew = nil, false
	return f.end(s.footer)
}

// fileStream writes the statements of one file. The separator after a row is
// decided when the row is added but written when the next row, or the end of
// the file, arrives: the last row always closes its statement.
type fileStream struct {
	wc      io.WriteCloser
	bw      *bufio.Writer
	head    string
	opt     Options
	open    bool   // a statement is open
	size    int    // bytes of the open statement
	pending string // the separator owed after the previous row
}

func (f *fileStream) add(row []string) error {
	if f.pending != "" {
		if _, err := f.bw.WriteString(f.pending); err != nil {
			return err
		}
	}
	if !f.open {
		if _, err := f.bw.WriteString(f.head); err != nil {
			return err
		}
		f.size = len(f.head)
		f.open = true
	}
	// Write the row field by field and hash the same bytes as
	// "(" + strings.Join(row, ",") + ")", with no joined string or hasher
	// allocated. bufio.Writer keeps its first error, so it is checked once.
	h, n := fnvAdd(fnvOffset32, "("), 2 // n: the bytes of the row text
	_, _ = f.bw.WriteString("(")
	for i, v := range row {
		if i > 0 {
			h, n = fnvAdd(h, ","), n+1
			_, _ = f.bw.WriteString(",")
		}
		h, n = fnvAdd(h, v), n+len(v)
		_, _ = f.bw.WriteString(v)
	}
	h = fnvAdd(h, ")")
	if _, err := f.bw.WriteString(")"); err != nil {
		return err
	}
	f.size += n + 2 // n is the row text; 2 is the ",\n" or ";\n" after it (as before B-017, R-143)
	if h%f.opt.RowsPerInsert == 0 || f.size >= f.opt.MaxInsertBytes {
		f.pending, f.open = ";\n", false
	} else {
		f.pending = ",\n"
	}
	return nil
}

// end closes the last statement, writes the footer, flushes and closes the
// file. The file is closed even when writing failed.
func (f *fileStream) end(footer string) (err error) {
	defer func() {
		if cerr := f.wc.Close(); err == nil {
			err = cerr
		}
	}()
	if f.pending != "" {
		if _, err = f.bw.WriteString(";\n"); err != nil {
			return err
		}
	}
	if _, err = f.bw.WriteString(footer); err != nil {
		return err
	}
	return f.bw.Flush()
}
