package dump

import (
	"context"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/writer"
)

func TestDataFileNamesRejectsNoFile(t *testing.T) {
	_, err := dataFileNames("d", "t", 0)
	if err == nil || !strings.Contains(err.Error(), `"t"`) {
		t.Fatalf("dataFileNames = %v, want an error naming table t", err)
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
