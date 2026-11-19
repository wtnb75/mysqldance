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
	if c.now == nil {
		c.now = realClock.now
	}
	if c.newTicker == nil {
		c.newTicker = realClock.newTicker
	}
	p := &progress{log: log, start: c.now(), running: map[string]bool{}}
	ticks, stopTicker := c.newTicker(interval)
	ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Go(func() {
		defer stopTicker()
		p.loop(ctx, ticks)
	})
	return p
}

// loop logs one record per tick until ctx is done.
func (p *progress) loop(ctx context.Context, ticks <-chan time.Time) {
	last := p.start
	lastRows := int64(0)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			lastRows = p.report(now, last, lastRows)
			last = now
		}
	}
}

// stop ends the goroutine and waits for it, so no record follows the call. It is
// safe to call twice and on nil.
func (p *progress) stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.cancel()
		p.wg.Wait()
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

// report logs one record at time now and returns the row count it saw. since is
// the time of the previous record (or of the start), and sinceRows its rows, for
// the speed of the last interval.
func (p *progress) report(now, since time.Time, sinceRows int64) int64 {
	rows, bytes := p.rows.Load(), p.bytes.Load()
	p.mu.Lock()
	tables, done, approx := p.tables, p.done, p.approx
	running := make([]string, 0, len(p.running))
	for name := range p.running {
		running = append(running, name)
	}
	p.mu.Unlock()
	slices.Sort(running)

	attrs := []any{"rows", rows, "bytes", bytes, "tables_done", done, "tables", tables, "approx_rows", approx}
	// approx_rows is the TABLE_ROWS estimate: without one there is nothing to
	// compare with, and once the rows pass it the estimate was too low, so the
	// percentage stops at 99 and no time is promised.
	if approx > 0 {
		pct := math.Floor(float64(rows)*1000/float64(approx)) / 10
		if rows >= approx {
			pct = 99
		}
		attrs = append(attrs, "percent", pct)
	}
	attrs = append(attrs, "rows_per_sec", ratePerSec(rows-sinceRows, now.Sub(since)))
	if elapsed := now.Sub(p.start); approx > 0 && rows > 0 && rows < approx && elapsed > 0 {
		remaining := float64(approx-rows) / (float64(rows) / elapsed.Seconds())
		// A huge estimate with a slow start would overflow time.Duration: no time is promised then.
		if remaining < float64(math.MaxInt64)/float64(time.Second) {
			attrs = append(attrs, "eta", time.Duration(remaining*float64(time.Second)).Round(time.Second))
		}
	}
	attrs = append(attrs, "running", strings.Join(running, ","))
	p.log.Info("dump progress", attrs...)
	return rows
}

// ratePerSec is n per d, rounded to a whole number; 0 for an empty interval.
func ratePerSec(n int64, d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(math.Round(float64(n) / d.Seconds()))
}
