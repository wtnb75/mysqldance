package kanon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"mysqldance/internal/mask"
)

func TestSummarize(t *testing.T) {
	// 3 groups of size 1, 2 of size 2, 1 of size 5, 1 of size 7, 1 of 50, 1 of 100, 1 of 2000.
	sizes := []SizeCount{
		{Size: 1, Groups: 3}, {Size: 2, Groups: 2}, {Size: 5, Groups: 1}, {Size: 7, Groups: 1},
		{Size: 50, Groups: 1}, {Size: 100, Groups: 1}, {Size: 2000, Groups: 1},
	}
	got := Summarize("db", "t", []string{"a", "b"}, sizes, 0)
	if got.Rows != 3+4+5+7+50+100+2000 || got.Groups != 10 {
		t.Errorf("rows = %d, groups = %d", got.Rows, got.Groups)
	}
	if got.MinK == nil || *got.MinK != 1 {
		t.Errorf("MinK = %v, want 1", got.MinK)
	}
	want := Distribution{K1: 3, K2: 4, K3: 0, K4: 0, K5: 5, K6to9: 7, K10to99: 50, K100Plus: 2100}
	if got.Dist != want {
		t.Errorf("distribution = %+v, want %+v", got.Dist, want)
	}
	if got.Below != nil {
		t.Errorf("Below = %v, want none without a threshold", *got.Below)
	}
}

func TestSummarizeBelowThreshold(t *testing.T) {
	sizes := []SizeCount{{Size: 1, Groups: 2}, {Size: 3, Groups: 1}, {Size: 4, Groups: 5}}
	for _, tt := range []struct {
		k    int64
		want int64
	}{{1, 0}, {2, 2}, {3, 2}, {4, 5}, {5, 25}} {
		got := Summarize("db", "t", []string{"a"}, sizes, tt.k)
		if got.Below == nil || *got.Below != tt.want {
			t.Errorf("k=%d: Below = %v, want %d", tt.k, got.Below, tt.want)
		}
	}
}

func TestSummarizeEmptyTable(t *testing.T) {
	got := Summarize("db", "t", []string{"a"}, nil, 5)
	if got.Rows != 0 || got.Groups != 0 || got.MinK != nil {
		t.Errorf("an empty table must have no rows, no groups and no minimum: %+v", got)
	}
	// An empty table is not a violation.
	if got.Below == nil || *got.Below != 0 {
		t.Errorf("Below = %v, want 0", got.Below)
	}
}

func sample() []TableReport {
	one := int64(1)
	two := int64(2)
	return []TableReport{
		Summarize("a", "users", []string{"zip", "born"}, []SizeCount{{Size: 1, Groups: 1}, {Size: 2, Groups: 3}}, 2),
		{DB: "a", Table: "empty", Columns: []string{"x"}, Below: &two},
		{DB: "b", Table: "t", Columns: []string{"y"}, Rows: 1, Groups: 1, MinK: &one, Dist: Distribution{K1: 1}},
	}
}

func TestWriteTable(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTable(&buf, sample(), true); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want a header and 3 rows, got:\n%s", got)
	}
	if !strings.HasPrefix(lines[0], "DATABASE") || !strings.Contains(lines[0], "BELOW") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "zip,born") {
		t.Errorf("columns missing from %q", lines[1])
	}
	if !strings.Contains(lines[2], "-") {
		t.Errorf("an empty table has no minimum k, shown as -: %q", lines[2])
	}
	var again bytes.Buffer
	if err := WriteTable(&again, sample(), true); err != nil || again.String() != got {
		t.Errorf("the table is not deterministic")
	}
}

func TestWriteTableWithoutThreshold(t *testing.T) {
	var buf bytes.Buffer
	reports := sample()
	for i := range reports {
		reports[i].Below = nil
	}
	if err := WriteTable(&buf, reports, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "BELOW") {
		t.Errorf("no threshold, no BELOW column:\n%s", buf.String())
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("one line per table, got:\n%s", buf.String())
	}
	want0 := `{"database":"a","table":"users","columns":["zip","born"],"rows":7,"groups":4,"min_k":1,` +
		`"distribution":{"1":1,"2":6,"3":0,"4":0,"5":0,"6-9":0,"10-99":0,"100+":0},"below":1}`
	if lines[0] != want0 {
		t.Errorf("line 0 = %s\nwant     %s", lines[0], want0)
	}
	if !strings.Contains(lines[1], `"min_k":null`) {
		t.Errorf("an empty table has a null minimum: %s", lines[1])
	}
	if strings.Contains(lines[2], "below") {
		t.Errorf("no threshold, no below: %s", lines[2])
	}
}

func TestAnyBelow(t *testing.T) {
	if AnyBelow(nil) {
		t.Error("nothing measured is not a violation")
	}
	if !AnyBelow(sample()) {
		t.Error("a table with rows below the threshold is a violation")
	}
	z := int64(0)
	if AnyBelow([]TableReport{{Below: &z}, {}}) {
		t.Error("0 rows below, and no threshold, are not violations")
	}
}

type fakeCounter struct {
	sizes map[string][]SizeCount
	err   error
	calls []string
}

func (f *fakeCounter) Use(_ context.Context, db string) error {
	f.calls = append(f.calls, "use "+db)
	return nil
}

func (f *fakeCounter) GroupSizes(_ context.Context, table string, cols []mask.QuasiColumn) ([]SizeCount, error) {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	f.calls = append(f.calls, "count "+table+"("+strings.Join(names, ",")+")")
	return f.sizes[table], f.err
}

func TestMeasure(t *testing.T) {
	tables := []mask.QuasiTable{
		{DB: "a", Table: "t1", Columns: []mask.QuasiColumn{{Name: "x"}, {Name: "y"}}},
		{DB: "a", Table: "t2", Columns: []mask.QuasiColumn{{Name: "z"}}},
		{DB: "b", Table: "t3", Columns: []mask.QuasiColumn{{Name: "w"}}},
	}
	f := &fakeCounter{sizes: map[string][]SizeCount{
		"t1": {{Size: 1, Groups: 1}, {Size: 4, Groups: 2}},
		"t3": {{Size: 3, Groups: 1}},
	}}
	got, err := Measure(context.Background(), f, tables, 3)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := "use a|count t1(x,y)|count t2(z)|use b|count t3(w)"
	if strings.Join(f.calls, "|") != wantCalls {
		t.Errorf("calls = %v, want %s", f.calls, wantCalls)
	}
	if len(got) != 3 || got[0].Rows != 9 || *got[0].Below != 1 || got[1].MinK != nil || *got[2].Below != 0 {
		t.Errorf("reports = %+v", got)
	}
}

func TestMeasureFailureNamesTheTableAndNoValue(t *testing.T) {
	f := &fakeCounter{err: errors.New("boom")}
	_, err := Measure(context.Background(), f, []mask.QuasiTable{{DB: "a", Table: "t", Columns: []mask.QuasiColumn{{Name: "x"}}}}, 0)
	if err == nil || !strings.Contains(err.Error(), "a.t") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v", err)
	}
}
