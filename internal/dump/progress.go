package dump

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mysqldance/internal/source"
)

// DefaultProgressInterval is the ProgressInterval of the command line when
// --progress-interval is not given.
const DefaultProgressInterval = 10 * time.Second

// clock is the time source of the progress records, replaced in tests.
type clock struct {
	now func() time.Time
	// newTicker returns a channel that delivers the time of each tick, and a
	// function that stops the ticker.
	newTicker func(d time.Duration) (<-chan time.Time, func())
}

var realClock = clock{
	now: time.Now,
	newTicker: func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTicker(d)
		return t.C, t.Stop
	},
}

// progress is the running totals of a dump, read by a ticker goroutine that logs
// them every interval. The counters of the hot path (one add per row, one per
// write) are atomic, so the row loop takes no lock. A nil *progress ignores every
// call, which is what a run without progress records and the tests of writeTable
// use. It never holds a row value, only counts and table names.
type progress struct {
	rows, bytes atomic.Int64

	mu      sync.Mutex
	tables  int             // tables to be output, of the databases read so far
	done    int             // tables whose data files are written
	approx  int64           // sum of TableInfo.ApproxRows of those tables
	running map[string]bool // "db.table" of the tables being read

	log   *slog.Logger
	bar   *ProgressBar // drawn instead of logging; nil when records are logged
	start time.Time

	stopOnce sync.Once
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// startProgress starts the goroutine that logs a "dump progress" record every
// interval. It returns nil when interval is not positive. The caller must call
// stop before it logs "dump finished" and on every other way out of the run.
func startProgress(ctx context.Context, log *slog.Logger, interval time.Duration, c clock) *progress {
	if interval <= 0 {
		return nil
	}
	p := &progress{log: log, running: map[string]bool{}}
	// The speed of a record is the one of the last interval.
	var last time.Time // zero until the first tick, which then counts from the start
	var lastRows int64
	p.launch(ctx, interval, c, func(now time.Time) {
		if last.IsZero() {
			last = p.start
		}
		lastRows = p.report(now, last, lastRows)
		last = now
	})
	return p
}

// barInterval is how often the bar is drawn. It is fixed, not --progress-interval,
// which only paces the records.
const barInterval = 250 * time.Millisecond

// startProgressBar starts the goroutine that draws the bar every barInterval and
// logs nothing. It returns nil without a bar. The caller must call stop on every
// way out of the run, which erases the bar.
func startProgressBar(ctx context.Context, bar *ProgressBar, c clock) *progress {
	if bar == nil {
		return nil
	}
	p := &progress{bar: bar, running: map[string]bool{}}
	p.launch(ctx, barInterval, c, func(now time.Time) {
		// The speed of the bar is the average of the run: a speed over 250ms
		// would jump with every batch.
		bar.draw(p.stats(now, p.start, 0).barLine())
	})
	return p
}

// launch starts the goroutine that calls tick with the time of each tick of
// interval until stop.
func (p *progress) launch(ctx context.Context, interval time.Duration, c clock, tick func(now time.Time)) {
	if c.now == nil {
		c.now = realClock.now
	}
	if c.newTicker == nil {
		c.newTicker = realClock.newTicker
	}
	p.start = c.now()
	ticks, stopTicker := c.newTicker(interval)
	ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Go(func() {
		defer stopTicker()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticks:
				tick(now)
			}
		}
	})
}

// stop ends the goroutine and waits for it, so no record follows the call, and
// erases the bar. It is safe to call twice and on nil.
func (p *progress) stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.cancel()
		p.wg.Wait()
		if p.bar != nil {
			p.bar.clear()
		}
	})
}

// addTables counts the tables that db will output.
func (p *progress) addTables(tables []source.TableInfo) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tables += len(tables)
	for _, t := range tables {
		p.approx += t.ApproxRows
	}
}

// begin marks the table as being read; end marks it as written (done) or not.
func (p *progress) begin(db, table string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.running[db+"."+table] = true
	p.mu.Unlock()
}

func (p *progress) end(db, table string, done bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.running, db+"."+table)
	if done {
		p.done++
	}
	p.mu.Unlock()
}

func (p *progress) addRow() {
	if p != nil {
		p.rows.Add(1)
	}
}

// bytesCounter is the counter countingFile adds to; nil when there is no
// progress.
func (p *progress) bytesCounter() *atomic.Int64 {
	if p == nil {
		return nil
	}
	return &p.bytes
}

// progressStats is what a "dump progress" record and the bar both show. It holds
// only counts and table names, never a row value.
type progressStats struct {
	rows, bytes        int64
	tablesDone, tables int
	approx             int64
	percent            float64
	hasPercent         bool
	rate               int64 // rows per second
	eta                time.Duration
	hasEta             bool
	running            []string // "db.table", sorted
}

// stats reads the totals at time now. since is the time of the previous record
// (or of the start), and sinceRows its rows, for the speed since then.
func (p *progress) stats(now, since time.Time, sinceRows int64) progressStats {
	s := progressStats{rows: p.rows.Load(), bytes: p.bytes.Load()}
	p.mu.Lock()
	s.tables, s.tablesDone, s.approx = p.tables, p.done, p.approx
	s.running = make([]string, 0, len(p.running))
	for name := range p.running {
		s.running = append(s.running, name)
	}
	p.mu.Unlock()
	slices.Sort(s.running)

	// approx is the TABLE_ROWS estimate: without one there is nothing to
	// compare with, and once the rows pass it the estimate was too low, so the
	// percentage stops at 99 and no time is promised.
	if s.approx > 0 {
		s.hasPercent = true
		s.percent = math.Floor(float64(s.rows)*1000/float64(s.approx)) / 10
		if s.rows >= s.approx {
			s.percent = 99
		}
	}
	s.rate = ratePerSec(s.rows-sinceRows, now.Sub(since))
	if elapsed := now.Sub(p.start); s.approx > 0 && s.rows > 0 && s.rows < s.approx && elapsed > 0 {
		remaining := float64(s.approx-s.rows) / (float64(s.rows) / elapsed.Seconds())
		// A huge estimate with a slow start would overflow time.Duration: no time is promised then.
		if remaining < float64(math.MaxInt64)/float64(time.Second) {
			s.hasEta = true
			s.eta = time.Duration(remaining * float64(time.Second)).Round(time.Second)
		}
	}
	return s
}

// report logs one record at time now and returns the row count it saw.
func (p *progress) report(now, since time.Time, sinceRows int64) int64 {
	s := p.stats(now, since, sinceRows)
	attrs := []any{"rows", s.rows, "bytes", s.bytes, "tables_done", s.tablesDone, "tables", s.tables, "approx_rows", s.approx}
	if s.hasPercent {
		attrs = append(attrs, "percent", s.percent)
	}
	attrs = append(attrs, "rows_per_sec", s.rate)
	if s.hasEta {
		attrs = append(attrs, "eta", s.eta)
	}
	attrs = append(attrs, "running", strings.Join(s.running, ","))
	p.log.Info("dump progress", attrs...)
	return s.rows
}

// ratePerSec is n per d, rounded to a whole number; 0 for an empty interval.
func ratePerSec(n int64, d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(math.Round(float64(n) / d.Seconds()))
}
