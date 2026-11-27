package source

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"mysqldance/internal/extsort"
)

func TestCellsQueryHasNoOrderByAndNamesOnlyTheColumns(t *testing.T) {
	q := cellsQuery("`shop`.`users`", []string{"email", "we`ird"})
	if want := "SELECT `email`, `we``ird` FROM `shop`.`users`"; q != want {
		t.Errorf("query = %q, want %q", q, want)
	}
	if strings.Contains(strings.ToUpper(q), "ORDER BY") || strings.Contains(q, "*") {
		t.Errorf("query orders or selects every column: %q", q)
	}
}

// noScratch fails the test when anything asks for a scratch file: a scan reads
// the raw rows of production, which must not be written anywhere.
func noScratch(t *testing.T) extsort.NewFile {
	return func() (extsort.File, error) {
		t.Fatal("a scratch file was created")
		return nil, errors.New("unreachable")
	}
}

func TestEachCellsOfPassesNullAsNilAndNeverSpills(t *testing.T) {
	// A budget of one byte would spill at once if the rows went through extsort.
	s := &Snapshot{sortMemory: 1, sortFile: noScratch(t)}
	rows := &fakeRows{rows: [][][]byte{
		{[]byte("b@example.com"), nil},
		{[]byte("a@example.com"), []byte("x")},
	}}
	var got []string
	err := s.eachCellsOf(rows, 2, func(cells []sql.RawBytes) error {
		if len(cells) != 2 {
			t.Fatalf("cells = %d", len(cells))
		}
		got = append(got, string(cells[0])+"|"+map[bool]string{true: "NULL", false: string(cells[1])}[cells[1] == nil])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The order of the server is kept: nothing sorts.
	if want := "b@example.com|NULL,a@example.com|x"; strings.Join(got, ",") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

func TestEachCellsOfStopsOnTheCallbackError(t *testing.T) {
	s := &Snapshot{}
	boom := errors.New("boom")
	calls := 0
	err := s.eachCellsOf(&fakeRows{rows: [][][]byte{{[]byte("1")}, {[]byte("2")}}}, 1, func([]sql.RawBytes) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Errorf("err = %v after %d calls", err, calls)
	}
}
