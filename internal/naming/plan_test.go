package naming

import (
	"sort"
	"strings"
	"testing"
)

func TestPlanLeavesShortNamesAlone(t *testing.T) {
	p := NewPlan("db", []string{"t1", "日本"})
	if got, want := p.SchemaFile(), SchemaFile("db"); got != want {
		t.Errorf("SchemaFile = %q, want %q", got, want)
	}
	if got, want := p.DataFile("日本"), DataFile("db", "日本"); got != want {
		t.Errorf("DataFile = %q, want %q", got, want)
	}
	if p.DBShortened() || p.TableShortened("t1", 1) || p.TableShortened("日本", 1) {
		t.Error("a name within the limit was reported as shortened")
	}
	// Exactly 255 bytes is within the limit.
	tbl := strings.Repeat("a", MaxFileNameBytes-len("db")-len(dataInfix)-len(sqlExt))
	if got := NewPlan("db", []string{tbl}).DataFile(tbl); len(got) != MaxFileNameBytes || got != DataFile("db", tbl) {
		t.Errorf("a %d-byte name was changed: %q", MaxFileNameBytes, got)
	}
}

func TestPlanShortensLongTable(t *testing.T) {
	tbl := strings.Repeat("日", 40) // 360 escaped bytes
	p := NewPlan("db", []string{tbl, "other"})
	got := p.DataFile(tbl)
	if len(got) > MaxFileNameBytes {
		t.Fatalf("%d bytes, want at most %d: %s", len(got), MaxFileNameBytes, got)
	}
	if !p.TableShortened(tbl, 1) || p.DBShortened() {
		t.Errorf("TableShortened = %v, DBShortened = %v", p.TableShortened(tbl, 1), p.DBShortened())
	}
	if again := NewPlan("db", []string{tbl}).DataFile(tbl); again != got {
		t.Errorf("not deterministic: %q vs %q", got, again)
	}
	// The cut never lands inside a %XX.
	comp := strings.TrimSuffix(strings.TrimPrefix(got, "db"+dataInfix), sqlExt)
	head, _, ok := strings.Cut(comp, "~")
	if !ok || strings.HasSuffix(head, "%") || (len(head) >= 2 && head[len(head)-2] == '%') {
		t.Errorf("bad cut in %q", comp)
	}
	// Different tables with the same prefix get different names.
	other := tbl + "x"
	if NewPlan("db", []string{other}).DataFile(other) == got {
		t.Error("two long names share a shortened name")
	}
}

func TestPlanShortensLongDatabaseForAllFiles(t *testing.T) {
	db := strings.Repeat("日", 20)  // 180 escaped bytes
	tbl := strings.Repeat("本", 10) // 90 escaped bytes: db is the longer
	p := NewPlan(db, []string{tbl, "s"})
	if !p.DBShortened() || p.TableShortened(tbl, 1) {
		t.Fatalf("DBShortened = %v, TableShortened = %v", p.DBShortened(), p.TableShortened(tbl, 1))
	}
	names := []string{p.SchemaFile(), p.RoutinesFile(), p.TriggersFile(), p.ConstraintsFile(), p.DataFile(tbl), p.DataFile("s")}
	names = append(names, p.DataFileParts(tbl, 3)...)
	for _, n := range names {
		if len(n) > MaxFileNameBytes {
			t.Errorf("%d bytes: %s", len(n), n)
		}
		if !p.IsOwned(n) {
			t.Errorf("%s is not owned by its own plan", n)
		}
	}
	// Schema < data < routines < triggers < constraints in lexicographic order.
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	n := len(sorted)
	if sorted[0] != p.SchemaFile() || sorted[n-1] != p.ConstraintsFile() || sorted[n-2] != p.TriggersFile() || sorted[n-3] != p.RoutinesFile() {
		t.Errorf("order broken: %v", sorted)
	}
}

func TestPlanBothLong(t *testing.T) {
	db := strings.Repeat("日", 20)
	tbl := strings.Repeat("本", 20)
	p := NewPlan(db, []string{tbl})
	for _, n := range append(p.DataFileParts(tbl, 1), p.DataFileParts(tbl, 12345)...) {
		if len(n) > MaxFileNameBytes {
			t.Errorf("%d bytes: %s", len(n), n)
		}
	}
	if !p.DBShortened() || !p.TableShortened(tbl, 1) {
		t.Error("both names should be shortened")
	}
}

func TestPlanOwnershipSurvivesShorteningChange(t *testing.T) {
	db := strings.Repeat("日", 20)
	// An earlier dump with a short table list left a file under the full name
	// prefix; a plan that now shortens the database still owns it.
	old := EscapeName(db) + ".00-schema.sql"
	p := NewPlan(db, []string{strings.Repeat("本", 20)})
	if !p.IsOwned(old) {
		t.Errorf("%s is not owned", old)
	}
	if p.IsOwned("other.00-schema.sql") {
		t.Error("a foreign file is owned")
	}
}

func TestPlanOwnsFilesOfAnEarlierShortenedName(t *testing.T) {
	db := strings.Repeat("a", 200)
	long, short := strings.Repeat("b", 60), "b"
	// With the long table the database part is shortened; once the table is
	// gone it is not, and the files of the earlier dump must still be found.
	before := NewPlan(db, []string{long})
	if !before.DBShortened() {
		t.Fatal("the database part was not shortened")
	}
	after := NewPlan(db, []string{short})
	if after.DBShortened() {
		t.Fatal("the database part was shortened without need")
	}
	for _, n := range []string{before.SchemaFile(), before.DataFile(long), before.ConstraintsFile()} {
		if !after.IsOwned(n) {
			t.Errorf("%s of the earlier dump is not owned", n)
		}
	}
}

func TestPlanShortensDatabaseWhenTableCannotFit(t *testing.T) {
	db := strings.Repeat("a", 230)  // a bit shorter than the table, but long
	tbl := strings.Repeat("b", 240) // would have 12 bytes left to shorten to
	p := NewPlan(db, []string{tbl})
	for _, n := range p.DataFileParts(tbl, 1) {
		if len(n) > MaxFileNameBytes {
			t.Errorf("%d bytes: %s", len(n), n)
		}
	}
	if !p.DBShortened() {
		t.Error("the database part should be shortened")
	}
}
