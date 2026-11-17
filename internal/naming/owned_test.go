package naming

import (
	"strings"
	"testing"
)

// A plan owns the files of its own database in every form, and never the files of
// another database, including one that differs only in case or in an escape.
func TestPlanNeverOwnsFilesOfAnotherDatabase(t *testing.T) {
	long := strings.Repeat("Ab", 100) // shortened in both forms
	dbs := []string{"a", "A", "ab", "Ab", "%41", "%2541", "a.b", "a%2Eb", "U", "%55", long, strings.ToLower(long)}
	for _, x := range dbs {
		for _, caseSafe := range []bool{false, true} {
			px := NewPlanCaseSafe(x, []string{"T", "t"}, caseSafe)
			files := []string{px.SchemaFile(), px.DataFile("T"), px.DataFile("t"), px.ConstraintsFile(), px.TriggersFile()}
			for _, y := range dbs {
				for _, py := range []*Plan{NewPlan(y, []string{"T", "t"}), NewPlanCaseSafe(y, []string{"T", "t"}, true)} {
					for _, f := range files {
						if got, want := py.IsOwned(f), x == y; got != want {
							t.Errorf("plan of %q (caseSafe=%v): IsOwned(%q) of plan for %q = %v, want %v", x, caseSafe, f, y, got, want)
						}
					}
				}
			}
		}
	}
}
