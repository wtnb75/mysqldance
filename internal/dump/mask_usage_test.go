package dump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/mask"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
	"mysqldance/internal/writer"
)

// nullRows is a source of n rows whose column s is NULL, masked as
// source.Snapshot.EachRow does.
type nullRows struct{ n int }

func (r nullRows) EachRow(_ context.Context, t source.TableInfo, fn func([]sqlvalue.Value) error) error {
	for i := range r.n {
		row := []sqlvalue.Value{
			sqlvalue.NewValue("INT", []byte{byte('0' + i%10)}, false),
			sqlvalue.NewValue("VARCHAR", nil, true),
		}
		if t.Mask != nil {
			if err := t.Mask(row); err != nil {
				return err
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

// usagePlan reads n rows of table d.t with the rule on s, through writeTable,
// and returns the plan with its counters.
func usagePlan(t *testing.T, src rowSource) *mask.Plan {
	t.Helper()
	reg := mask.DefaultRegistry()
	rules, err := mask.Parse([]byte("version: 1\nrules:\n  d.t.s:\n    use: fixed\n    value: hidden-value\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := rules.Resolve(streamCatalog(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	opt := Options{OutDir: dir, Log: testLogger(io.Discard), Writer: writer.NewMySQL(datafile.Options{}), maskPlan: plan}
	st := &stage{dir: dir}
	if _, err := writeTable(context.Background(), opt, src, naming.NewPlan("d", []string{"t"}), "d", streamTable, st); err != nil {
		t.Fatal(err)
	}
	if err := st.discard(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestReportMaskUsage(t *testing.T) {
	tests := []struct {
		name       string
		src        rowSource
		wantInfo   string
		wantWarn   bool
		strictFail bool
	}{
		{"used", maskingRows{fakeRows{n: 3}}, "rows_hit=3 table_rows=3", false, false},
		{"every value NULL", nullRows{n: 2}, "rows_hit=0 table_rows=2", true, true},
		{"empty table", nullRows{n: 0}, "rows_hit=0 table_rows=0", false, false},
	}
	for _, tt := range tests {
		for _, strict := range []bool{false, true} {
			t.Run(tt.name+map[bool]string{false: "", true: "/strict"}[strict], func(t *testing.T) {
				plan := usagePlan(t, tt.src)
				var log bytes.Buffer
				err := reportMaskUsage(testLogger(&log), plan, strict)
				out := log.String()
				if !strings.Contains(out, "level=INFO") && !tt.wantWarn {
					t.Errorf("no INFO record:\n%s", out)
				}
				if !strings.Contains(out, tt.wantInfo) || !strings.Contains(out, "rule=d.t.s") || !strings.Contains(out, "use=fixed") {
					t.Errorf("log lacks %q with the rule:\n%s", tt.wantInfo, out)
				}
				if got := strings.Contains(out, "level=WARN"); got != tt.wantWarn {
					t.Errorf("WARN = %v, want %v:\n%s", got, tt.wantWarn, out)
				}
				if strings.Contains(out, "hidden-value") {
					t.Errorf("the log holds a value:\n%s", out)
				}
				switch {
				case strict && tt.strictFail:
					if !errors.Is(err, ErrUnusedRules) || !strings.Contains(err.Error(), "d.t.s") {
						t.Errorf("err = %v, want ErrUnusedRules naming d.t.s", err)
					}
				case err != nil:
					t.Errorf("err = %v, want nil (strict %v)", err, strict)
				}
			})
		}
	}
}

func TestReportMaskUsageWithoutPlan(t *testing.T) {
	var log bytes.Buffer
	if err := reportMaskUsage(testLogger(&log), nil, true); err != nil || log.Len() != 0 {
		t.Errorf("err = %v, log = %q, want nothing", err, log.String())
	}
}

// StrictRules without rules is refused before anything is opened.
func TestStrictRulesNeedsRules(t *testing.T) {
	err := Run(context.Background(), Options{DBs: []string{"d"}, OutDir: t.TempDir(), StrictRules: true})
	if err == nil || !strings.Contains(err.Error(), "StrictRules needs Mask") {
		t.Errorf("err = %v", err)
	}
}

// A run that fails is not judged by --strict-rules: the error is the failure's.
func TestStrictRulesDoesNotHideAFailure(t *testing.T) {
	reg := mask.DefaultRegistry()
	rules, err := mask.Parse([]byte("version: 1\nrules:\n  d.t.s:\n    use: nullify\n"), reg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := source.Config{Host: "127.0.0.1", Port: 1, User: "u"}
	err = Run(context.Background(), Options{Source: cfg, DBs: []string{"d"}, OutDir: t.TempDir(), Mask: rules, StrictRules: true, Log: testLogger(io.Discard)})
	if err == nil || errors.Is(err, ErrUnusedRules) {
		t.Errorf("err = %v, want the connection failure", err)
	}
}
