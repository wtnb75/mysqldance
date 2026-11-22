// Package kanon measures k-anonymity (B-196): for the columns marked quasi in
// the masking rules, how many rows share each combination of values. Only counts
// are read and reported; no value leaves the server.
package kanon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"text/tabwriter"

	"mysqldance/internal/mask"
	"mysqldance/internal/source"
)

// SizeCount says that Groups combinations of values are each shared by Size rows.
type SizeCount = source.GroupSize

// Counter is what Measure reads from: a *source.Snapshot, or a fake in tests.
type Counter interface {
	// Use selects the database the next table is read from.
	Use(ctx context.Context, db string) error
	// GroupSizes returns how many groups of each size the rows of the table form
	// when grouped by the columns, in the order of the size.
	GroupSizes(ctx context.Context, table string, cols []mask.QuasiColumn) ([]SizeCount, error)
}

// Distribution is the number of rows (not groups) whose k, the size of the group
// they are in, falls in each range.
type Distribution struct {
	K1, K2, K3, K4, K5, K6to9, K10to99, K100Plus int64
}

// MarshalJSON writes the ranges in order, so the line is the same on every run.
func (d Distribution) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, `{"1":%d,"2":%d,"3":%d,"4":%d,"5":%d,"6-9":%d,"10-99":%d,"100+":%d}`,
		d.K1, d.K2, d.K3, d.K4, d.K5, d.K6to9, d.K10to99, d.K100Plus), nil
}

func (d Distribution) fields() [8]int64 {
	return [8]int64{d.K1, d.K2, d.K3, d.K4, d.K5, d.K6to9, d.K10to99, d.K100Plus}
}

func (d *Distribution) add(size, rows int64) {
	switch {
	case size == 1:
		d.K1 += rows
	case size == 2:
		d.K2 += rows
	case size == 3:
		d.K3 += rows
	case size == 4:
		d.K4 += rows
	case size == 5:
		d.K5 += rows
	case size < 10:
		d.K6to9 += rows
	case size < 100:
		d.K10to99 += rows
	default:
		d.K100Plus += rows
	}
}

// TableReport is what is measured of one table. MinK is nil for a table with no
// rows. Below, set only when a threshold was given, is the number of rows whose
// k is under it.
type TableReport struct {
	DB      string       `json:"database"`
	Table   string       `json:"table"`
	Columns []string     `json:"columns"`
	Rows    int64        `json:"rows"`
	Groups  int64        `json:"groups"`
	MinK    *int64       `json:"min_k"`
	Dist    Distribution `json:"distribution"`
	Below   *int64       `json:"below,omitempty"`
}

// Summarize turns the group sizes of a table into its report. k is the threshold
// (0 for none).
func Summarize(db, table string, columns []string, sizes []SizeCount, k int64) TableReport {
	r := TableReport{DB: db, Table: table, Columns: columns}
	var below int64
	for _, s := range sizes {
		rows := s.Size * s.Groups
		r.Rows += rows
		r.Groups += s.Groups
		r.Dist.add(s.Size, rows)
		if r.MinK == nil || s.Size < *r.MinK {
			size := s.Size
			r.MinK = &size
		}
		if s.Size < k {
			below += rows
		}
	}
	if k > 0 {
		r.Below = &below
	}
	return r
}

// AnyBelow reports whether a table has a row whose k is under the threshold.
func AnyBelow(reports []TableReport) bool {
	for _, r := range reports {
		if r.Below != nil && *r.Below > 0 {
			return true
		}
	}
	return false
}

// Measure measures each table, in the order given. A failure names the table and
// never carries a value.
func Measure(ctx context.Context, c Counter, tables []mask.QuasiTable, k int64) ([]TableReport, error) {
	reports := make([]TableReport, 0, len(tables))
	db := ""
	for _, t := range tables {
		// The first table always differs from the empty name, so it selects its database.
		if t.DB != db {
			if err := c.Use(ctx, t.DB); err != nil {
				return nil, fmt.Errorf("database %q: %w", t.DB, err)
			}
			db = t.DB
		}
		names := make([]string, len(t.Columns))
		for j, col := range t.Columns {
			names[j] = col.Name
		}
		sizes, err := c.GroupSizes(ctx, t.Table, t.Columns)
		if err != nil {
			return nil, fmt.Errorf("table %s.%s: %w", t.DB, t.Table, err)
		}
		reports = append(reports, Summarize(t.DB, t.Table, names, sizes, k))
	}
	return reports, nil
}

// Options is what Run needs.
type Options struct {
	Source        source.Config
	DBs           []string
	ExcludeTables []string
	// Rules holds the columns marked quasi.
	Rules *mask.Rules
	// K is the threshold; 0 reports only.
	K   int64
	Log *slog.Logger
}

// Run measures every table that has a column marked quasi, in one snapshot, as
// of the same point in time as a dump of the same databases would show. It reads
// no row.
func Run(ctx context.Context, opt Options) (reports []TableReport, err error) {
	if opt.Rules == nil {
		return nil, errors.New("no rules")
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	cfg := opt.Source
	cfg.Log = opt.Log
	cfg.ExcludeTables = opt.ExcludeTables
	snap, err := source.Open(ctx, cfg, opt.DBs...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, snap.Close()) }()
	cat, err := snap.MaskCatalog(ctx, opt.DBs)
	if err != nil {
		return nil, err
	}
	tables, err := opt.Rules.QuasiTables(cat, mask.DefaultRegistry())
	if err != nil {
		return nil, fmt.Errorf("mask rules: %w", err)
	}
	return Measure(ctx, snap, tables, opt.K)
}

// WriteTable writes the reports as a table of text. withBelow adds the column of
// the rows under the threshold.
func WriteTable(w io.Writer, reports []TableReport, withBelow bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := []string{"DATABASE", "TABLE", "COLUMNS", "ROWS", "GROUPS", "MIN_K",
		"K=1", "K=2", "K=3", "K=4", "K=5", "K=6-9", "K=10-99", "K=100+"}
	if withBelow {
		head = append(head, "BELOW")
	}
	if _, err := fmt.Fprintln(tw, strings.Join(head, "\t")); err != nil {
		return err
	}
	for _, r := range reports {
		minK := "-"
		if r.MinK != nil {
			minK = strconv.FormatInt(*r.MinK, 10)
		}
		cells := []string{r.DB, r.Table, strings.Join(r.Columns, ","),
			strconv.FormatInt(r.Rows, 10), strconv.FormatInt(r.Groups, 10), minK}
		for _, n := range r.Dist.fields() {
			cells = append(cells, strconv.FormatInt(n, 10))
		}
		if withBelow {
			below := "-"
			if r.Below != nil {
				below = strconv.FormatInt(*r.Below, 10)
			}
			cells = append(cells, below)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// WriteJSON writes one JSON object per table, one per line.
func WriteJSON(w io.Writer, reports []TableReport) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range reports {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
