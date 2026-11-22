package dump

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func fixedColumns(n int) func() int { return func() int { return n } }

func TestBarLineWithEstimate(t *testing.T) {
	s := progressStats{
		rows: 12345, bytes: 3 << 20, tablesDone: 3, tables: 10,
		percent: 50, hasPercent: true, rate: 1234, eta: 62 * time.Second, hasEta: true,
		running: []string{"d.a", "d.b"},
	}
	want := "[=========>          ]  50.0%  12345 rows  3.0 MiB  1234 rows/s  eta 1m2s  tables 3/10  d.a,d.b"
	if got := s.barLine(); got != want {
		t.Errorf("barLine:\n%q\nwant:\n%q", got, want)
	}
}

// Without an estimate there is no percentage, no bar and no eta, only the counts.
func TestBarLineWithoutEstimate(t *testing.T) {
	s := progressStats{rows: 10, bytes: 512, tables: 1, rate: 5, running: []string{"d.a"}}
	want := "10 rows  512 B  5 rows/s  tables 0/1  d.a"
	if got := s.barLine(); got != want {
		t.Errorf("barLine:\n%q\nwant:\n%q", got, want)
	}
}

// With --jobs above 1 several tables are read; the line names the first three and counts the rest.
func TestBarLineNamesFirstThreeRunningTables(t *testing.T) {
	s := progressStats{tables: 9, running: []string{"d.a", "d.b", "d.c", "d.d", "d.e"}}
	if got := s.barLine(); !strings.HasSuffix(got, "d.a,d.b,d.c +2") {
		t.Errorf("barLine = %q, want the first three tables and +2", got)
	}
}

func TestBarCells(t *testing.T) {
	for _, tt := range []struct {
		pct  float64
		want string
	}{
		{0, "[                    ]"},
		{4.9, "[                    ]"},
		{5, "[>                   ]"},
		{99, "[==================> ]"},
		{100, "[===================>]"},
	} {
		s := progressStats{percent: tt.pct, hasPercent: true}
		if got := s.barLine(); !strings.HasPrefix(got, tt.want) {
			t.Errorf("percent %v: %q, want prefix %q", tt.pct, got, tt.want)
		}
	}
}

func TestStatsMatchTheLogRecord(t *testing.T) {
	fc := newFakeClock()
	p := startProgress(context.Background(), testLogger(&bytes.Buffer{}), time.Hour, fc.clock())
	defer p.stop()
	p.addTables(tables(1000, 3000))
	p.begin("d", "b")
	p.begin("d", "a")
	addRows(p, 500)
	s := p.stats(fc.t0.Add(10*time.Second), fc.t0, 0)
	if s.rows != 500 || s.percent != 12.5 || !s.hasPercent || s.rate != 50 || !s.hasEta || s.eta != 70*time.Second ||
		strings.Join(s.running, ",") != "d.a,d.b" {
		t.Errorf("stats = %+v", s)
	}
}

// The bar is drawn on one line with a carriage return and no ANSI escape, once
// per tick; the end of the run erases it by overwriting with spaces.
func TestProgressBarDrawsEachTickAndErasesOnStop(t *testing.T) {
	var out bytes.Buffer
	var period time.Duration
	fc := newFakeClock()
	c := fc.clock()
	c.newTicker = func(d time.Duration) (<-chan time.Time, func()) { period = d; return fc.ticks, func() {} }
	bar := NewProgressBar(&out, fixedColumns(200))
	p := startProgressBar(context.Background(), bar, c)
	if period != 250*time.Millisecond {
		t.Errorf("bar period = %v, want 250ms", period)
	}
	p.addTables(tables(100))
	addRows(p, 50)
	fc.tick(time.Second)
	p.stop()
	got := out.String()
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\n") {
		t.Errorf("output %q holds an escape or a newline", got)
	}
	// "\r" line "\r" spaces "\r": one draw, then the erase.
	parts := strings.Split(got, "\r")
	if len(parts) != 4 || parts[0] != "" || parts[3] != "" {
		t.Fatalf("output %q, want one draw and one erase", got)
	}
	if !strings.HasPrefix(parts[1], "[") || !strings.Contains(parts[1], " 50.0%  50 rows") {
		t.Errorf("bar line %q", parts[1])
	}
	if parts[2] != strings.Repeat(" ", utf8.RuneCountInString(parts[1])) {
		t.Errorf("erase %q does not cover the bar line %q", parts[2], parts[1])
	}
	p.stop() // idempotent
}

func TestProgressBarTruncatesToTheWidth(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(30))
	bar.draw(strings.Repeat("x", 100))
	if want := "\r" + strings.Repeat("x", 29); out.String() != want {
		t.Errorf("output %q, want %q (columns - 1 characters)", out.String(), want)
	}
}

// A shorter line is padded so that nothing of the longer one before is left.
func TestProgressBarPadsAShorterLine(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(80))
	bar.draw("abcdef")
	out.Reset()
	bar.draw("abc")
	if want := "\rabc   "; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}
}

// A log record written while the bar is on screen: the bar is erased, the record
// is written whole, and the bar is drawn again below it.
func TestProgressBarLogWriteErasesAndRedraws(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(80))
	bar.draw("abc")
	out.Reset()
	if _, err := bar.Write([]byte("log line\n")); err != nil {
		t.Fatal(err)
	}
	if want := "\r   \rlog line\n\rabc"; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}
}

// With no bar on screen a write is passed through unchanged.
func TestProgressBarWriteWithoutBar(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(80))
	if _, err := bar.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "x\n" {
		t.Errorf("output %q, want the record only", out.String())
	}
}

// After clear the bar stays off: a later write or draw leaves no bar behind.
func TestProgressBarClearIsFinal(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(80))
	bar.draw("abc")
	bar.clear()
	out.Reset()
	bar.draw("late")
	if _, err := bar.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "after\n" {
		t.Errorf("output %q, want only the record", out.String())
	}
}

// Records written from several goroutines and the bar do not interleave inside a line.
func TestProgressBarSerializesWrites(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(80))
	bar.draw("bar")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, _ = bar.Write([]byte("record\n"))
			bar.draw("bar")
		})
	}
	wg.Wait()
	// What a terminal shows: every finished line is exactly one record.
	lines := strings.Split(out.String(), "\n")
	for _, l := range lines[:len(lines)-1] {
		if got := screenLine(l); got != "record" {
			t.Errorf("a finished line shows %q, want record", got)
		}
	}
	if len(lines)-1 != 20 {
		t.Errorf("%d records, want 20", len(lines)-1)
	}
}

// screenLine is what a terminal shows for a line of output: a carriage return
// moves to the start of the line and the text after it overwrites the old.
func screenLine(l string) string {
	var screen []rune
	col := 0
	for _, r := range l {
		if r == '\r' {
			col = 0
			continue
		}
		if col < len(screen) {
			screen[col] = r
		} else {
			screen = append(screen, r)
		}
		col++
	}
	return strings.TrimRight(string(screen), " ")
}

// The bar does not log: with a bar, no "dump progress" record is written.
func TestProgressBarLogsNoRecord(t *testing.T) {
	var out, log bytes.Buffer
	fc := newFakeClock()
	bar := NewProgressBar(&out, fixedColumns(80))
	p := startProgressBar(context.Background(), bar, fc.clock())
	p.log = testLogger(&log)
	fc.tick(time.Second)
	p.stop()
	if log.Len() != 0 {
		t.Errorf("log = %q, want nothing", log.String())
	}
}

func TestStartProgressBarNilBarIsNil(t *testing.T) {
	if p := startProgressBar(context.Background(), nil, clock{}); p != nil {
		t.Fatal("no bar must not start progress")
	}
}

// A full-width character takes two columns: the cut counts columns, not
// characters, or the line wraps and a carriage return cannot erase it.
func TestProgressBarCutsByColumns(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(11))
	bar.draw(strings.Repeat("顧", 20))
	if want := "\r" + strings.Repeat("顧", 5); out.String() != want {
		t.Errorf("output %q, want %q (10 columns)", out.String(), want)
	}
	out.Reset()
	bar.clear()
	if want := "\r" + strings.Repeat(" ", 10) + "\r"; out.String() != want {
		t.Errorf("erase %q, want %q", out.String(), want)
	}
}

// A control character in a table name (newline, tab, ESC) would move the cursor
// or start a terminal sequence; the line shows a question mark instead.
func TestProgressBarReplacesControlCharacters(t *testing.T) {
	var out bytes.Buffer
	bar := NewProgressBar(&out, fixedColumns(80))
	bar.draw("d.a\n\x1b[2Jb\tc")
	if want := "\rd.a??[2Jb?c"; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tt := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{3 << 20, "3.0 MiB"}, {5 << 30, "5.0 GiB"}, {2 << 40, "2.0 TiB"},
	} {
		if got := formatBytes(tt.n); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
