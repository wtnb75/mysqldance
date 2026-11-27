// Package scanrun scans the values of the columns of a database with the rules of
// package scan (B-177) and reports where it found something: the database, table,
// column and rule, and how many rows matched. It never reports, logs or returns
// a value, a part of one, or the position of a row.
package scanrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"mysqldance/internal/scan"
	"mysqldance/internal/source"
)

// Finding says that the rows of a column matched a rule: Rows is how many rows
// had a match (a row counts once however many times the rule matched in it), and
// Scanned is how many rows of the column were scanned (a NULL is not).
type Finding struct {
	DB      string `json:"database"`
	Table   string `json:"table"`
	Column  string `json:"column"`
	Rule    string `json:"rule"`
	Rows    int64  `json:"rows"`
	Scanned int64  `json:"scanned"`
}

// Reader is what Run reads from: a *source.Snapshot behind Open, or a fake in
// tests.
type Reader interface {
	// Use selects the database whose tables are read next.
	Use(ctx context.Context, db string) error
	// Tables returns the base tables of the selected database.
	Tables(ctx context.Context) ([]source.TableInfo, error)
	// EachCells reads the named columns of the rows of a table, unsorted and
	// without scratch files. The cells are valid until fn returns.
	EachCells(ctx context.Context, table string, columns []string, fn func(cells []sql.RawBytes) error) error
}

// A denylist on purpose (B-177, review ledger R-239): a type that is not listed is
// scanned, so an unknown text type is never skipped without a word.
//
// notText are the TypeNames (database/sql's DatabaseTypeName) of the columns that
// are not scanned: the bytes of a binary string, a bit string and a spatial value
// are not text that a rule written for text can read.
var notText = map[string]bool{
	"BINARY": true, "VARBINARY": true,
	"TINYBLOB": true, "BLOB": true, "MEDIUMBLOB": true, "LONGBLOB": true,
	"BIT":      true,
	"GEOMETRY": true, "POINT": true, "LINESTRING": true, "POLYGON": true,
	"MULTIPOINT": true, "MULTILINESTRING": true, "MULTIPOLYGON": true, "GEOMETRYCOLLECTION": true,
}

// Scannable reports whether a column of the type is scanned.
func Scannable(typeName string) bool { return !notText[strings.ToUpper(typeName)] }

// Run scans every table of dbs, in one pass over each, and returns the findings
// ordered by database, table, column and rule (bytewise), so the same data gives
// the same report. Only a (column, rule) that matched at least one row is listed.
// A failure names the table and never carries a value.
func Run(ctx context.Context, r Reader, cfg *scan.Config, dbs []string) ([]Finding, error) {
	var out []Finding
	for _, db := range dbs {
		if err := r.Use(ctx, db); err != nil {
			return nil, fmt.Errorf("database %q: %w", db, err)
		}
		tables, err := r.Tables(ctx)
		if err != nil {
			return nil, fmt.Errorf("database %q: %w", db, err)
		}
		for _, t := range tables {
			found, err := scanTable(ctx, r, cfg, db, t)
			if err != nil {
				return nil, fmt.Errorf("table %s.%s: %w", db, t.Name, err)
			}
			out = append(out, found...)
		}
	}
	slices.SortFunc(out, func(a, b Finding) int {
		return strings.Compare(a.DB+"\x00"+a.Table+"\x00"+a.Column+"\x00"+a.Rule,
			b.DB+"\x00"+b.Table+"\x00"+b.Column+"\x00"+b.Rule)
	})
	return out, nil
}

// scanTable reads the scannable columns of one table.
func scanTable(ctx context.Context, r Reader, cfg *scan.Config, db string, t source.TableInfo) ([]Finding, error) {
	var names []string
	for _, c := range t.Columns {
		if Scannable(c.TypeName) {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = db + "." + t.Name + "." + n
	}
	scanned := make([]int64, len(names))
	rows := make([]map[string]int64, len(names))
	// The rules that matched in one cell, so that a rule counts a row once.
	seen := map[string]bool{}
	err := r.EachCells(ctx, t.Name, names, func(cells []sql.RawBytes) error {
		for i, cell := range cells {
			if cell == nil {
				continue
			}
			scanned[i]++
			clear(seen)
			for _, m := range cfg.Scan(paths[i], string(cell)) {
				if seen[m.RuleID] {
					continue
				}
				seen[m.RuleID] = true
				if rows[i] == nil {
					rows[i] = map[string]int64{}
				}
				rows[i][m.RuleID]++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for i, byRule := range rows {
		for rule, n := range byRule {
			out = append(out, Finding{DB: db, Table: t.Name, Column: names[i], Rule: rule, Rows: n, Scanned: scanned[i]})
		}
	}
	return out, nil
}

// Options is what Execute needs.
type Options struct {
	Source        source.Config
	DBs           []string
	ExcludeTables []string
	// Config holds the rules.
	Config *scan.Config
	Log    *slog.Logger
}

// Execute scans the databases in one snapshot, as of the same point in time as a
// dump of the same databases would show.
func Execute(ctx context.Context, opt Options) (findings []Finding, err error) {
	if opt.Config == nil {
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
	return Run(ctx, snapshotReader{snap: snap, log: opt.Log}, opt.Config, opt.DBs)
}

// snapshotReader adapts a Snapshot to Reader: it logs what Tables warns about.
type snapshotReader struct {
	snap *source.Snapshot
	log  *slog.Logger
}

func (r snapshotReader) Use(ctx context.Context, db string) error { return r.snap.Use(ctx, db) }

func (r snapshotReader) Tables(ctx context.Context) ([]source.TableInfo, error) {
	tables, warnings, err := r.snap.Tables(ctx)
	for _, w := range warnings {
		if w.Kind != source.WarnSort { // a scan does not sort, so a warning about the sort does not apply
			r.log.Warn(w.Message, "table", w.Table)
		}
	}
	return tables, err
}

func (r snapshotReader) EachCells(ctx context.Context, table string, columns []string, fn func([]sql.RawBytes) error) error {
	return r.snap.EachCells(ctx, table, columns, fn)
}

// WriteTable writes the findings as a table of text.
func WriteTable(w io.Writer, findings []Finding) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "DATABASE\tTABLE\tCOLUMN\tRULE\tROWS\tSCANNED"); err != nil {
		return err
	}
	for _, f := range findings {
		if _, err := fmt.Fprintln(tw, strings.Join([]string{f.DB, f.Table, f.Column, f.Rule,
			strconv.FormatInt(f.Rows, 10), strconv.FormatInt(f.Scanned, 10)}, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// WriteJSON writes one JSON object per finding, one per line.
func WriteJSON(w io.Writer, findings []Finding) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, f := range findings {
		if err := enc.Encode(f); err != nil {
			return err
		}
	}
	return nil
}
