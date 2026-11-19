package dump

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/writer"
)

// fakeClock starts at t0 and ticks only when the test sends on ticks. The
// channel is unbuffered, so a send returns after the goroutine took the tick,
// and the previous record is logged by the time a second send returns.
type fakeClock struct {
	t0    time.Time
	ticks chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t0: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ticks: make(chan time.Time)}
}

func (f *fakeClock) clock() clock {
	return clock{
		now:       func() time.Time { return f.t0 },
		newTicker: func(time.Duration) (<-chan time.Time, func()) { return f.ticks, func() {} },
	}
}

func (f *fakeClock) tick(after time.Duration) { f.ticks <- f.t0.Add(after) }

func tables(approx ...int64) []source.TableInfo {
	ts := make([]source.TableInfo, len(approx))
	for i, n := range approx {
		ts[i] = source.TableInfo{Name: string(rune('a' + i)), ApproxRows: n}
	}
	return ts
}

func addRows(p *progress, n int) {
	for range n {
		p.addRow()
	}
}

// progressLines returns the "dump progress" lines logged by then.
func progressLines(out string) []string {
	var lines []string
	for l := range strings.Lines(out) {
		if strings.Contains(l, "msg=\"dump progress\"") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	return lines
}

func TestProgressRecordEveryInterval(t *testing.T) {
	var buf bytes.Buffer
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&buf), 10*time.Second, fc.clock())
	p.addTables(tables(1000, 3000))
	p.begin("d", "b")
	p.begin("d", "a")
	addRows(p, 500)
	p.bytes.Add(4096)
	fc.tick(10 * time.Second)
	// The second tick is taken only after the first record is logged.
	p.end("d", "a", true)
	addRows(p, 500)
	fc.tick(20 * time.Second)
	p.stop()
	got := progressLines(buf.String())
	want := []string{
		`level=INFO msg="dump progress" rows=500 bytes=4096 tables_done=0 tables=2 approx_rows=4000 percent=12.5 rows_per_sec=50 eta=1m10s running=d.a,d.b`,
		`level=INFO msg="dump progress" rows=1000 bytes=4096 tables_done=1 tables=2 approx_rows=4000 percent=25 rows_per_sec=50 eta=1m0s running=d.b`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("records:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestProgressWithoutEstimateHasNoPercentOrEta(t *testing.T) {
	var buf bytes.Buffer
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&buf), time.Second, fc.clock())
	p.addTables(tables(0))
	addRows(p, 10)
	fc.tick(time.Second)
	p.stop()
	got := progressLines(buf.String())
	if len(got) != 1 || strings.Contains(got[0], "percent") || strings.Contains(got[0], "eta") {
		t.Errorf("records = %q, want one without percent and eta", got)
	}
}

func TestProgressPastTheEstimateStopsAt99(t *testing.T) {
	for _, rows := range []int{100, 150} {
		var buf bytes.Buffer
		fc := newFakeClock()
		p := startProgress(context.Background(), testLogger(&buf), time.Second, fc.clock())
		p.addTables(tables(100))
		addRows(p, rows)
		fc.tick(time.Second)
		p.stop()
		got := progressLines(buf.String())
		if len(got) != 1 || !strings.Contains(got[0], "percent=99 ") || strings.Contains(got[0], "eta") {
			t.Errorf("rows=%d: records = %q, want percent=99 and no eta", rows, got)
		}
	}
}

func TestProgressNoRecordBeforeTheFirstInterval(t *testing.T) {
	var buf bytes.Buffer
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&buf), time.Hour, fc.clock())
	addRows(p, 5)
	p.stop()
	p.stop() // idempotent
	if got := progressLines(buf.String()); len(got) != 0 {
		t.Errorf("records = %q, want none", got)
	}
}

func TestProgressDisabledIsNil(t *testing.T) {
	if p := startProgress(context.Background(), testLogger(&bytes.Buffer{}), 0, clock{}); p != nil {
		t.Fatal("interval 0 must not start progress")
	}
	// A nil progress ignores every call.
	var p *progress
	p.addTables(tables(1))
	p.begin("d", "t")
	p.addRow()
	p.end("d", "t", true)
	p.stop()
}

// writeTable feeds the counters: rows, the bytes of the files, the table that
// is running while it is read, and the tables that are done.
func TestWriteTableFeedsProgress(t *testing.T) {
	dir := t.TempDir()
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&bytes.Buffer{}), time.Hour, fc.clock())
	defer p.stop()
	opt := Options{OutDir: dir, Log: testLogger(&bytes.Buffer{}), Writer: writer.NewMySQL(datafile.Options{}), prog: p}
	st := &stage{dir: dir}
	var during map[string]bool
	rows := fakeRows{n: 100, each: func(i int) {
		if i == 50 {
			p.mu.Lock()
			during = map[string]bool{}
			for k := range p.running {
				during[k] = true
			}
			p.mu.Unlock()
		}
	}}
	res, err := writeTable(context.Background(), opt, rows, naming.NewPlan("d", []string{"t"}), "d", streamTable, st)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.rows.Load(); got != 100 {
		t.Errorf("rows = %d, want 100", got)
	}
	if got := p.bytes.Load(); got != res.bytes || got == 0 {
		t.Errorf("bytes = %d, want %d (the bytes of the files)", got, res.bytes)
	}
	if !during["d.t"] || len(during) != 1 {
		t.Errorf("running during the read = %v, want d.t", during)
	}
	if len(p.running) != 0 || p.done != 1 {
		t.Errorf("after: running = %v, done = %d, want none and 1", p.running, p.done)
	}
}

// A table that fails is no longer running and is not counted as done.
func TestWriteTableFailureEndsRunning(t *testing.T) {
	dir := t.TempDir()
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&bytes.Buffer{}), time.Hour, fc.clock())
	defer p.stop()
	opt := Options{OutDir: dir, Log: testLogger(&bytes.Buffer{}), Writer: writer.NewMySQL(datafile.Options{}), prog: p}
	st := &stage{dir: dir}
	defer func() { _ = st.discard() }()
	_, err := writeTable(context.Background(), opt, fakeRows{n: 10, failAt: 5, err: context.Canceled}, naming.NewPlan("d", []string{"t"}), "d", streamTable, st)
	if err == nil {
		t.Fatal("want the read error")
	}
	if len(p.running) != 0 || p.done != 0 {
		t.Errorf("running = %v, done = %d, want none and 0", p.running, p.done)
	}
}

// Below the estimate the percentage is the real one, even above 99; at the
// estimate it stops at 99.
func TestProgressPercentNearTheEstimate(t *testing.T) {
	var buf bytes.Buffer
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&buf), time.Second, fc.clock())
	p.addTables(tables(1000))
	addRows(p, 995)
	fc.tick(time.Second)
	p.stop()
	if got := progressLines(buf.String()); len(got) != 1 || !strings.Contains(got[0], "percent=99.5 ") {
		t.Errorf("records = %q, want percent=99.5", got)
	}
}

// An estimate so large that the remaining time overflows a Duration gives no eta.
func TestProgressHugeEstimateHasNoEta(t *testing.T) {
	var buf bytes.Buffer
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&buf), time.Second, fc.clock())
	p.addTables(tables(1 << 62))
	addRows(p, 1)
	fc.tick(10 * time.Second)
	p.stop()
	if got := progressLines(buf.String()); len(got) != 1 || strings.Contains(got[0], "eta") {
		t.Errorf("records = %q, want one without eta", got)
	}
}

// A clock with only one of its functions set still gets the real other one.
func TestStartProgressFillsClockFields(t *testing.T) {
	p := startProgress(context.Background(), testLogger(&bytes.Buffer{}), time.Hour, clock{now: time.Now})
	p.stop()
}
