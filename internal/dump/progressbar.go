package dump

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
)

// barCells is the width of the bar itself, between the brackets.
const barCells = 20

// maxBarTables is how many running tables the bar line names; the rest is counted.
const maxBarTables = 3

// ProgressBar draws one line on a terminal with carriage returns. It is also the
// io.Writer the log goes through while the bar is on screen: a write erases the
// bar, writes the record, and draws the bar again below it, all under one lock, so
// a record is never cut by the bar. Only a carriage return and spaces are used,
// no ANSI escape, so a console that knows no escape sequences shows it too.
type ProgressBar struct {
	out     io.Writer
	columns func() int

	mu     sync.Mutex
	line   string // the last line given to draw, drawn again after a write
	drawn  string // what is on the screen now; "" when nothing is
	closed bool   // after clear, nothing is drawn again
}

// NewProgressBar returns a bar that draws on out. columns gives the width of the
// terminal and is called at every draw, so a resized window is followed; a line
// is cut to columns() - 1 characters, because a line that wraps cannot be erased
// by a carriage return.
func NewProgressBar(out io.Writer, columns func() int) *ProgressBar {
	return &ProgressBar{out: out, columns: columns}
}

// Write erases the bar, passes p to the output and draws the bar again. p is a
// whole log record (it ends with a newline).
func (b *ProgressBar) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.eraseLocked()
	n, err := b.out.Write(p)
	b.drawLocked()
	return n, err
}

// draw replaces the bar on the screen with line.
func (b *ProgressBar) draw(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.line = line
	b.drawLocked()
}

// clear erases the bar for good: it is not drawn again by a later write or draw.
func (b *ProgressBar) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.eraseLocked()
	b.line, b.closed = "", true
}

func (b *ProgressBar) drawLocked() {
	if b.closed || b.line == "" {
		return
	}
	text, cols := fitColumns(b.line, max(b.columns()-1, 1))
	// Pad over what a longer previous line left.
	pad := max(textColumns(b.drawn)-cols, 0)
	// A failed write to the terminal is not worth stopping a dump for.
	_, _ = io.WriteString(b.out, "\r"+text+strings.Repeat(" ", pad))
	b.drawn = text
}

func (b *ProgressBar) eraseLocked() {
	if b.drawn == "" {
		return
	}
	_, _ = io.WriteString(b.out, "\r"+strings.Repeat(" ", textColumns(b.drawn))+"\r")
	b.drawn = ""
}

// fitColumns returns s cut to at most width terminal columns, with every control
// character replaced by "?" (a name from the database is shown on the line, and
// a newline, a tab or an escape would move the cursor), and the columns it takes.
func fitColumns(s string, width int) (string, int) {
	var sb strings.Builder
	cols := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			r = '?'
		}
		w := runeColumns(r)
		if cols+w > width {
			break
		}
		sb.WriteRune(r)
		cols += w
	}
	return sb.String(), cols
}

func textColumns(s string) int {
	n := 0
	for _, r := range s {
		n += runeColumns(r)
	}
	return n
}

// runeColumns is 2 for the East Asian wide and full-width characters and emoji,
// 1 for the rest. It is a small table instead of a library: a wrong guess only
// shortens or lengthens the cut by a column.
func runeColumns(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

// barLine is the line of the bar: the bar and the percentage when there is an
// estimate, then the counts, the speed, the time left, the tables done and the
// tables being read.
func (s progressStats) barLine() string {
	var sb strings.Builder
	if s.hasPercent {
		cells := int(s.percent / 100 * barCells)
		sb.WriteString("[")
		if cells == 0 {
			sb.WriteString(strings.Repeat(" ", barCells))
		} else {
			sb.WriteString(strings.Repeat("=", cells-1) + ">" + strings.Repeat(" ", barCells-cells))
		}
		fmt.Fprintf(&sb, "]  %.1f%%  ", s.percent)
	}
	fmt.Fprintf(&sb, "%d rows  %s  %d rows/s", s.rows, formatBytes(s.bytes), s.rate)
	if s.hasEta {
		fmt.Fprintf(&sb, "  eta %s", s.eta)
	}
	fmt.Fprintf(&sb, "  tables %d/%d", s.tablesDone, s.tables)
	if len(s.running) > 0 {
		names := s.running[:min(len(s.running), maxBarTables)]
		sb.WriteString("  " + strings.Join(names, ","))
		if rest := len(s.running) - len(names); rest > 0 {
			fmt.Fprintf(&sb, " +%d", rest)
		}
	}
	return sb.String()
}

// formatBytes is n in the largest binary unit that keeps it at 1 or above.
func formatBytes(n int64) string {
	const units = "KMGT"
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n)/1024, 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %ciB", v, units[i])
}
