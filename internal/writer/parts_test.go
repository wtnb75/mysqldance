package writer

import (
	"bytes"
	"io"

	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// Part writes one output file of a table's data.
type Part func(w io.Writer) error

type fileBuffer struct{ bytes.Buffer }

func (*fileBuffer) Close() error { return nil }

// dataParts runs rows through w.NewRowWriter and returns one Part per file it
// wrote. A failure (of the writer or of a row) is returned by the only Part, so
// a test sees it where it renders the file.
func dataParts(w Writer, db string, t source.TableInfo, rows [][]sqlvalue.Value) []Part {
	var files []*fileBuffer
	open := func() (io.WriteCloser, error) {
		f := &fileBuffer{}
		files = append(files, f)
		return f, nil
	}
	err := func() error {
		rw, err := w.NewRowWriter(db, t, open)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := rw.Write(row); err != nil {
				_ = rw.Close()
				return err
			}
		}
		return rw.Close()
	}()
	if err != nil {
		return []Part{func(io.Writer) error { return err }}
	}
	parts := make([]Part, len(files))
	for i, f := range files {
		parts[i] = func(w io.Writer) error { _, err := w.Write(f.Bytes()); return err }
	}
	return parts
}
