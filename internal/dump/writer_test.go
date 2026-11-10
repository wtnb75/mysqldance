package dump

import (
	"context"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
	"mysqldance/internal/writer"
)

// emptyPartsWriter breaks the Writer contract by returning no data file.
type emptyPartsWriter struct{ writer.Writer }

func (emptyPartsWriter) DataParts(string, source.TableInfo, [][]sqlvalue.Value) []writer.Part {
	return nil
}

func TestDataPartsRejectsEmptyResult(t *testing.T) {
	_, err := dataParts(emptyPartsWriter{}, "d", source.TableInfo{Name: "t"}, nil)
	if err == nil || !strings.Contains(err.Error(), `"t"`) {
		t.Fatalf("dataParts = %v, want an error naming table t", err)
	}
}

// Data configures the default writer only; setting it next to an explicit
// Writer must not be silently ignored.
func TestRunRejectsDataWithWriter(t *testing.T) {
	err := Run(context.Background(), Options{
		DBs:    []string{"d"},
		OutDir: t.TempDir(),
		Data:   datafile.Options{RowsPerInsert: 5},
		Writer: writer.NewMySQL(datafile.Options{}),
	})
	if err == nil || !strings.Contains(err.Error(), "Data") {
		t.Fatalf("Run = %v, want an error about Data and Writer", err)
	}
}
