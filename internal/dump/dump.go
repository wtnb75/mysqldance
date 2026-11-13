// Package dump writes the dump files of one or more databases into a flat
// output directory.
package dump

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mysqldance/internal/datafile"
	"mysqldance/internal/extsort"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
	"mysqldance/internal/writer"
)

// Options configures a dump run.
type Options struct {
	Source source.Config
	DBs    []string
	OutDir string
	// Data configures the default MySQL writer; it is ignored when Writer is
	// set.
	Data datafile.Options
	// Writer renders the dump files; nil means the MySQL writer built from Data.
	Writer writer.Writer
	// FileMode is the exact permission of the output files; 0 means 0666
	// filtered by the umask.
	FileMode os.FileMode
	// DirMode is the exact permission of the output directory when this run
	// creates it; 0 means 0755 filtered by the umask. An existing directory
	// is never changed.
	DirMode os.FileMode
	// SortMemory is about how many bytes of rows a table without a key may hold
	// in memory while it is sorted; past that the sort goes through scratch
	// files in OutDir. With Source.Jobs above 1, that many tables are sorted at
	// once. 0 means DefaultSortMemory.
	SortMemory int64
	// Log receives the progress (Info) and the warnings (Warn); nil discards
	// them. A record never carries a row value or a connection string.
	Log *slog.Logger
}

// DefaultSortMemory is the SortMemory used when none is set.
const DefaultSortMemory = 64 << 20

// sortMemoryOrDefault returns DefaultSortMemory for a SortMemory that is not
// positive: a negative limit would spill after every row.
func sortMemoryOrDefault(n int64) int64 {
	if n <= 0 {
		return DefaultSortMemory
	}
	return n
}

// Run dumps every database in opt.DBs into opt.OutDir, creating the
// directory if needed. For each database it validates all output file names,
// then writes the schema file and one data file per table under temporary
// names. Only when every database has been written are the files renamed into
// place and the files of earlier dumps that were not written again removed, so
// a failure before that leaves the previous dump untouched. Other files are
// left alone.
func Run(ctx context.Context, opt Options) (err error) {
	if opt.OutDir == "" {
		return errors.New("no output directory")
	}
	if len(opt.DBs) == 0 {
		return errors.New("no database to dump")
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	if opt.Writer == nil {
		opt.Writer = writer.NewMySQL(opt.Data)
	} else if opt.Data != (datafile.Options{}) {
		return errors.New("Options.Data configures the default writer and cannot be combined with Options.Writer")
	}
	// One connection and one transaction for all databases, so they all
	// come from the same point in time. Open comes before anything touches the
	// output directory: it checks the settings and every database (existence,
	// not a system database), so a bad one leaves the directory as it was,
	// not even created or swept.
	cfg := opt.Source
	cfg.Log = opt.Log
	// The stage only holds settings until a file is made, so it can exist before
	// Open; the sort of a table without a key makes its scratch files through it.
	st := &stage{dir: opt.OutDir, mode: opt.FileMode}
	cfg.SortFile, cfg.SortMemory = st.newScratch, sortMemoryOrDefault(opt.SortMemory)
	snap, err := source.Open(ctx, cfg, opt.DBs...)
	if err != nil {
		// Open covers every database (system-database check, existence,
		// lock decision), so the error is not attributed to the first one;
		// a "not found" error names its database itself (R-008).
		return err
	}
	defer func() { err = errors.Join(err, snap.Close()) }()
	if err := prepareOutDir(opt.OutDir, opt.DirMode); err != nil {
		return err
	}
	if err := sweepTemps(opt.OutDir); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, st.discard()) }()
	// Lower-cased output file name -> what it was generated for, across the
	// whole run, so names that differ only in case are rejected.
	seen := map[string]string{}
	// The file names of each database, for finding its files of earlier dumps.
	plans := map[string]*naming.Plan{}
	for i, db := range opt.DBs {
		if i > 0 {
			if err := snap.Use(ctx, db); err != nil {
				return fmt.Errorf("database %q: %w", db, err)
			}
		}
		opt.Log.Info("dumping database", "database", db)
		start := time.Now()
		var tot dbTotals
		if err := dumpDB(ctx, opt, snap, db, seen, st, plans, &tot); err != nil {
			return fmt.Errorf("database %q: %w", db, err)
		}
		// The files are staged, not yet in place: that is the last step of the run.
		opt.Log.Info("database dumped", "database", db, "tables", tot.tables,
			"rows", tot.rows, "bytes", tot.bytes, "files", tot.files, "duration", time.Since(start))
	}
	// Everything was read and written. From here on the output directory
	// changes, so stop only for a cancel that arrived before this point.
	if err := ctx.Err(); err != nil {
		return err
	}
	// The dump may have taken long, so check the owned names again before the
	// first rename.
	for _, db := range opt.DBs {
		if _, err := ownedEntries(opt.OutDir, plans[db]); err != nil {
			return fmt.Errorf("database %q: %w", db, err)
		}
	}
	if err := st.commit(); err != nil {
		return err
	}
	written := map[string]bool{}
	for _, f := range st.files {
		written[filepath.Base(f.final)] = true
	}
	for _, db := range opt.DBs {
		if err := removeStale(opt.OutDir, plans[db], written); err != nil {
			return fmt.Errorf("database %q: %w", db, err)
		}
	}
	return nil
}

// prepareOutDir creates dir if it is missing. A directory it creates gets
// exactly mode (0 = 0755 filtered by the umask); its missing parents always
// get the default. An existing directory is left as it is.
func prepareOutDir(dir string, mode os.FileMode) error {
	dir = filepath.Clean(dir)
	fi, err := os.Stat(dir)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return fmt.Errorf("output path %s is not a directory", dir)
		}
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	// The umask only removes bits, so the directory is never more open than
	// mode, even before the chmod that makes the mode exact.
	if err := os.Mkdir(dir, cmp.Or(mode, 0o755)); err != nil {
		return err
	}
	if mode == 0 {
		return nil
	}
	return os.Chmod(dir, mode)
}

// dbTotals is what the data files of one database hold.
type dbTotals struct {
	tables, files int
	rows, bytes   int64
}

// dumpDB stages the files of db and records its file names in plans. Once every
// table is written it sets tot to the totals of the data files. It runs in
// stages, in this order: read the objects of the database, validate every
// output file name, stage the schema file, the data files, and the other files.
func dumpDB(ctx context.Context, opt Options, snap *source.Snapshot, db string, seen map[string]string, st *stage, plans map[string]*naming.Plan, tot *dbTotals) error {
	schema, err := readSchema(ctx, opt, snap, db)
	if err != nil {
		return err
	}
	plan, err := planFiles(opt, db, schema, seen)
	if err != nil {
		return err
	}
	// Run reads plans only when every database was staged, so a failed
	// planFiles needs no entry.
	plans[db] = plan
	// Refuse owned entries that are not regular files before writing anything.
	if _, err := ownedEntries(opt.OutDir, plan); err != nil {
		return err
	}
	if err := st.create(filepath.Join(opt.OutDir, plan.SchemaFile()), func(w io.Writer) error {
		return opt.Writer.WriteSchema(w, schema)
	}); err != nil {
		return err
	}
	if err := writeTables(ctx, opt, snap, db, schema.Tables, plan, seen, st, tot); err != nil {
		return err
	}
	return writeExtraFiles(opt, schema, plan, st)
}

// readSchema reads everything the writer needs to describe db. Objects that
// the writer cannot output are dropped from the result with a warning, rather
// than left out silently.
func readSchema(ctx context.Context, opt Options, snap *source.Snapshot, db string) (writer.Schema, error) {
	createDB, err := snap.CreateDatabase(ctx)
	if err != nil {
		return writer.Schema{}, err
	}
	tables, warnings, err := snap.Tables(ctx)
	if err != nil {
		return writer.Schema{}, err
	}
	for _, w := range warnings {
		opt.Log.Warn(w, "database", db)
	}
	views, deps, err := snap.Views(ctx)
	if err != nil {
		return writer.Schema{}, err
	}
	// Every trigger is on a base table, and Tables returns all of them (R-088).
	triggers, err := snap.Triggers(ctx)
	if err != nil {
		return writer.Schema{}, err
	}
	if _, ok := opt.Writer.(writer.TriggerWriter); !ok {
		for _, t := range triggers {
			opt.Log.Warn(fmt.Sprintf("trigger %s on table %s is not output",
				sqlvalue.QuoteIdent(t.Name), sqlvalue.QuoteIdent(t.Table)),
				"database", db, "table", t.Table, "trigger", t.Name)
		}
		triggers = nil
	}
	routines, err := snap.Routines(ctx)
	if err != nil {
		return writer.Schema{}, err
	}
	if _, ok := opt.Writer.(writer.RoutineWriter); !ok {
		for _, r := range routines {
			opt.Log.Warn(fmt.Sprintf("%s %s is not output", strings.ToLower(r.Type), sqlvalue.QuoteIdent(r.Name)),
				"database", db, "routine", r.Name, "type", r.Type)
		}
		routines = nil
	}
	events, err := snap.Events(ctx)
	if err != nil {
		return writer.Schema{}, err
	}
	if _, ok := opt.Writer.(writer.EventWriter); !ok {
		for _, ev := range events {
			opt.Log.Warn(fmt.Sprintf("event %s is not output", sqlvalue.QuoteIdent(ev.Name)),
				"database", db, "event", ev.Name)
		}
		events = nil
	}
	var defs map[string]source.TableDef
	if opt.Writer.NeedsDefs() {
		if defs, err = snap.Defs(ctx); err != nil {
			return writer.Schema{}, err
		}
		for i := range tables {
			if d, ok := defs[tables[i].Name]; ok {
				tables[i].Def = &d
			}
		}
	}
	return writer.Schema{
		DB: db, CreateDatabase: createDB, Tables: tables, Views: views, ViewDeps: deps, Triggers: triggers, Routines: routines, Events: events, Defs: defs,
		Warn: func(msg string) { opt.Log.Warn(msg, "database", db) },
	}, nil
}

// planFiles decides the output file names of db and claims them in seen, so a
// name that is already taken is an error before anything is written. A table
// that is split is claimed again under its part names later (see writeTables).
func planFiles(opt Options, db string, schema writer.Schema, seen map[string]string) (*naming.Plan, error) {
	tableNames := make([]string, len(schema.Tables))
	for i, t := range schema.Tables {
		tableNames[i] = t.Name
	}
	plan := naming.NewPlan(db, tableNames)
	if plan.DBShortened() {
		opt.Log.Warn("the database part of the output file names is over the length limit and is shortened",
			"database", db, "original", naming.EscapeName(db), "shortened", plan.DBComponent())
	}
	if err := claim(seen, plan.SchemaFile(), fmt.Sprintf("the schema of database %q", db)); err != nil {
		return nil, err
	}
	for _, e := range extraFiles(opt.Writer, schema, plan) {
		if err := claim(seen, e.name, fmt.Sprintf("the %s of database %q", e.what, db)); err != nil {
			return nil, err
		}
	}
	for _, t := range schema.Tables {
		// This is the name of a single file; a table that is split is claimed
		// again under its part names (a provisional claim is safe: R-130).
		if err := claim(seen, plan.DataFile(t.Name), fmt.Sprintf("table %q of database %q", t.Name, db)); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// extraFile is a file of a database besides its schema and data files.
type extraFile struct {
	name  string // the file name, without the directory
	what  string // what it holds, for the message of a name clash
	write func(io.Writer) error
}

// extraFiles lists the routines, triggers, events and constraints files db
// gets, in the order they are claimed and staged. A database without routines,
// triggers or events has no such file, so the file of an earlier dump does not
// outlive them (see removeStale). The lists are nil for a writer that cannot
// write them (see readSchema). The constraints file is always there for a
// writer that has one, empty or not. It is a pure function of its inputs, so
// planFiles (claim) and writeExtraFiles (stage) see the same list.
func extraFiles(wr writer.Writer, schema writer.Schema, plan *naming.Plan) []extraFile {
	var files []extraFile
	if rw, ok := wr.(writer.RoutineWriter); ok && len(schema.Routines) > 0 {
		files = append(files, extraFile{plan.RoutinesFile(), "routines", func(w io.Writer) error { return rw.WriteRoutines(w, schema) }})
	}
	if tw, ok := wr.(writer.TriggerWriter); ok && len(schema.Triggers) > 0 {
		files = append(files, extraFile{plan.TriggersFile(), "triggers", func(w io.Writer) error { return tw.WriteTriggers(w, schema) }})
	}
	if ew, ok := wr.(writer.EventWriter); ok && len(schema.Events) > 0 {
		files = append(files, extraFile{plan.EventsFile(), "events", func(w io.Writer) error { return ew.WriteEvents(w, schema) }})
	}
	if cw, ok := wr.(writer.ConstraintWriter); ok {
		files = append(files, extraFile{plan.ConstraintsFile(), "constraints", func(w io.Writer) error { return cw.WriteConstraints(w, schema) }})
	}
	return files
}

// writeTables stages the data file(s) of every table and sets tot to their
// totals. The tables are read and written in parallel, one per reader. Whatever
// order they finish in, the output is the same: file contents depend on the
// table alone, and the checks below run in table order.
func writeTables(ctx context.Context, opt Options, snap *source.Snapshot, db string, tables []source.TableInfo, plan *naming.Plan, seen map[string]string, st *stage, tot *dbTotals) error {
	results := make([]tableResult, len(tables))
	errs := eachTable(ctx, snap.Readers(), len(tables), func(ctx context.Context, r *source.Snapshot, i int) error {
		var err error
		results[i], err = writeTable(ctx, opt, r, plan, db, tables[i], st)
		return err
	})
	if err := firstError(ctx, errs); err != nil {
		return err
	}
	// A cancel before the first table started leaves no error behind.
	if err := ctx.Err(); err != nil {
		return err
	}
	*tot = dbTotals{tables: len(tables)}
	for i, t := range tables {
		names := results[i].names
		tot.files += len(names)
		tot.rows += results[i].rows
		tot.bytes += results[i].bytes
		// The final names are known only now; say what is shortened for the
		// files that are written (a part number can shorten the table part
		// further than it is for a single file).
		if plan.TableShortened(t.Name, len(names)) {
			warnTableShortened(opt.Log, db, t.Name, plan.TableComponent(t.Name, len(names)))
		}
		if len(names) <= 1 {
			continue
		}
		// Claim the part names like the others, before anything is committed.
		for _, n := range names {
			if err := claim(seen, n, fmt.Sprintf("table %q of database %q", t.Name, db)); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeExtraFiles stages the files of extraFiles.
func writeExtraFiles(opt Options, schema writer.Schema, plan *naming.Plan, st *stage) error {
	for _, e := range extraFiles(opt.Writer, schema, plan) {
		if err := st.create(filepath.Join(opt.OutDir, e.name), e.write); err != nil {
			return err
		}
	}
	return nil
}

// rowSource is what writeTable reads rows from: a *source.Snapshot, or a fake
// in tests.
type rowSource interface {
	EachRow(ctx context.Context, t source.TableInfo, fn func(row []sqlvalue.Value) error) error
}

// tableResult is what writeTable wrote for a table: the final names of its data
// files (without the directory), the rows, and the bytes of the files.
type tableResult struct {
	names []string
	rows  int64
	bytes int64
}

// countingFile counts the bytes written to a staged data file into *n. Only
// Write and Close are passed on: the writers take an io.WriteCloser and use
// nothing else (R-177).
type countingFile struct {
	io.WriteCloser
	n *int64
}

func (c countingFile) Write(p []byte) (int, error) {
	n, err := c.WriteCloser.Write(p)
	*c.n += int64(n)
	return n, err
}

// writeTable streams the rows of t from r into the writer, stages the data
// files, and logs the table as dumped (Info: rows, bytes, files, duration; never
// a value). No row is held: each one is written as it is read. The number of
// files is known only at the end, and the names depend on it, so the files are
// renamed to their final names then.
func writeTable(ctx context.Context, opt Options, r rowSource, plan *naming.Plan, db string, t source.TableInfo, st *stage) (tableResult, error) {
	start := time.Now()
	// Hooks and error messages see this path for every file of the table.
	provisional := filepath.Join(opt.OutDir, plan.DataFile(t.Name))
	var staged []int
	var res tableResult
	open := func() (io.WriteCloser, error) {
		tf, err := st.open(provisional)
		if err != nil {
			return nil, err
		}
		staged = append(staged, tf.idx)
		return countingFile{tf, &res.bytes}, nil
	}
	rw, err := opt.Writer.NewRowWriter(db, t, open)
	if err != nil {
		return tableResult{}, err
	}
	// A failure of the writer is reported as it is; a failure of the read is
	// worded with the table.
	var writeErr error
	readErr := r.EachRow(ctx, t, func(row []sqlvalue.Value) error {
		if writeErr = rw.Write(row); writeErr == nil {
			res.rows++
		}
		return writeErr
	})
	closeErr := rw.Close()
	switch {
	case writeErr != nil:
		return tableResult{}, writeErr
	case readErr != nil:
		return tableResult{}, fmt.Errorf("table %q: %w", t.Name, readErr)
	case closeErr != nil:
		return tableResult{}, closeErr
	}
	names, err := dataFileNames(plan, t.Name, len(staged))
	if err != nil {
		return tableResult{}, err
	}
	for i, idx := range staged {
		st.setFinal(idx, filepath.Join(opt.OutDir, names[i]))
	}
	res.names = names
	// Progress, not a result: the files are staged and the run can still fail
	// before the commit (R-174).
	opt.Log.Info("table dumped", "database", db, "table", t.Name,
		"rows", res.rows, "bytes", res.bytes, "files", len(names), "duration", time.Since(start))
	return res, nil
}

// dataFileNames returns the final names of the n data files of a table. A
// writer that wrote none would leave the table without a file and no error, so
// that is refused.
func dataFileNames(plan *naming.Plan, table string, n int) ([]string, error) {
	if n == 0 {
		return nil, fmt.Errorf("table %q: the writer produced no data file", table)
	}
	return plan.DataFileParts(table, n), nil
}

// eachTable calls fn(ctx, reader, i) for i in [0, n), on len(readers)
// goroutines at a time, each with a reader of its own, and returns the error
// of every call (nil for the calls that succeeded or never ran). After the first
// failure no new call starts and ctx is canceled for the running ones. It
// returns when all goroutines are done, so the readers are free again.
func eachTable(ctx context.Context, readers []*source.Snapshot, n int, fn func(ctx context.Context, r *source.Snapshot, i int) error) []error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make([]error, n)
	var next atomic.Int64
	var wg sync.WaitGroup
	for _, r := range readers {
		wg.Go(func() {
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				if errs[i] = fn(ctx, r, i); errs[i] != nil {
					cancel()
				}
			}
		})
	}
	wg.Wait()
	return errs
}

// firstError returns the error of the first table (in table order) that
// failed for a reason of its own. A table that failed only because eachTable
// canceled it after another one had failed is not the cause, so it is skipped
// unless ctx itself was canceled. With several real failures the one reported
// can still depend on timing (the first failure cancels the tables still
// running); serially it is always the first table in order.
func firstError(ctx context.Context, errs []error) error {
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if first == nil {
			first = err
		}
		if ctx.Err() != nil || !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return first
}

// claim checks the length of an output file name and records it in seen,
// failing if another output file has the same name ignoring case.
func claim(seen map[string]string, name, what string) error {
	if len(name) > naming.MaxFileNameBytes {
		return fmt.Errorf("output file name for %s is %d bytes, over the %d-byte limit: %s",
			what, len(name), naming.MaxFileNameBytes, name)
	}
	// Escaped names are ASCII, so lower-casing is exact.
	key := strings.ToLower(name)
	if prev, ok := seen[key]; ok {
		return fmt.Errorf("output file %s for %s collides case-insensitively with the file for %s",
			name, what, prev)
	}
	seen[key] = what
	return nil
}

// warnTableShortened says that the table part of the data file names of a table
// is shortened. Names that are shortened because they would collide are
// reported by claim.
func warnTableShortened(log *slog.Logger, db, table, shortened string) {
	log.Warn("the table part of the output file names is over the length limit and is shortened",
		"database", db, "table", table, "original", naming.EscapeName(table), "shortened", shortened)
}

// ownedEntries returns the names of the entries in dir that belong to the dump
// of the database of plan. An entry with an owned name that is not a regular
// file (a symlink, a directory, ...) is an error.
func ownedEntries(dir string, plan *naming.Plan) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var owned []string
	for _, e := range entries {
		if !plan.IsOwned(e.Name()) {
			continue
		}
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("refusing to replace %s: not a regular file", filepath.Join(dir, e.Name()))
		}
		owned = append(owned, e.Name())
	}
	return owned, nil
}

// removeStale deletes the files of the earlier dump of the database of plan
// that this run did not write again.
func removeStale(dir string, plan *naming.Plan, written map[string]bool) error {
	owned, err := ownedEntries(dir, plan)
	if err != nil {
		return err
	}
	for _, name := range owned {
		if written[name] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// Temporary files live in the output directory, so that renaming them into
// place never crosses a file system. The names are short (a final name can
// already be 255 bytes), not *.sql (so docker-entrypoint-initdb.d skips them)
// and recognizable, so a run killed before it could clean up is swept by the
// next one.
const (
	tmpPrefix = ".mysqldance-"
	tmpSuffix = ".tmp"
)

// isTempName reports whether name is one of the temporary names of a run:
// those of staged files and of the sort's scratch files.
func isTempName(name string) bool {
	return strings.HasPrefix(name, tmpPrefix) && strings.HasSuffix(name, tmpSuffix)
}

// sweepTemps removes the temporary files an earlier run left behind.
func sweepTemps(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && isTempName(n) {
			if err := os.Remove(filepath.Join(dir, n)); err != nil {
				return err
			}
		}
	}
	return nil
}

// stage collects the output of a run under temporary names. Nothing at a
// final path changes until commit, so a run that fails earlier leaves the
// previous dump as it was.
type stage struct {
	dir  string
	mode os.FileMode // 0 = 0666 filtered by the umask
	// mu guards n and files: create is called by several goroutines (one per
	// reader). n numbers the temporary names.
	mu    sync.Mutex
	n     int
	files []stagedFile
	// scratch holds the scratch files of the sort that are not removed yet. They
	// are never renamed into place: discard removes what is left.
	scratch map[string]*scratchFile
}

type stagedFile struct {
	tmp, final string
	renamed    bool
}

// create writes a new temporary file with write, destined for the final path.
// The temporary file must not exist (O_EXCL never follows a symlink).
// Permissions follow the umask unless s.mode is set, which is applied exactly.
func (s *stage) create(final string, write func(io.Writer) error) error {
	tf, err := s.open(final)
	if err != nil {
		return err
	}
	err = write(tf.w)
	if err != nil {
		err = fmt.Errorf("write %s: %w", tf.label, err)
	}
	return tf.finish(err)
}

// tempFile is a staged file whose contents are written incrementally. Its
// final path may be set after the contents are written (see setFinal).
type tempFile struct {
	idx   int // index in the stage's files
	f     *os.File
	w     io.Writer
	label string // the path in error messages
}

// open creates a new temporary file and records it before anything is written,
// so a partial file is removed by discard. final is the path the file is
// renamed to unless setFinal says otherwise; fileWriter and error messages
// see it as the file's path.
func (s *stage) open(final string) (*tempFile, error) {
	idx := 0
	f, err := s.openTemp("", os.O_WRONLY, func(tmp string) {
		s.files = append(s.files, stagedFile{tmp: tmp, final: final})
		idx = len(s.files) - 1
	})
	if err != nil {
		return nil, err
	}
	return &tempFile{idx: idx, f: f, w: fileWriter(final, f), label: final}, nil
}

// openTemp creates a new temporary file in the stage's directory, named
// .mysqldance-<infix>NNNN.tmp, with the mode of the output files (the umask
// filters the mode given to OpenFile, so an exact mode is set afterwards).
// record is called, with the lock held, once the file exists and before anything
// is written, so that a partial file is removed by discard. The number in the
// name only has to be unique: staged files are renamed to their final names.
func (s *stage) openTemp(infix string, flag int, record func(tmp string)) (*os.File, error) {
	s.mu.Lock()
	s.n++
	tmp := filepath.Join(s.dir, fmt.Sprintf("%s%s%04d%s", tmpPrefix, infix, s.n, tmpSuffix))
	s.mu.Unlock()
	f, err := os.OpenFile(tmp, flag|os.O_CREATE|os.O_EXCL, cmp.Or(s.mode, 0o666))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	record(tmp)
	s.mu.Unlock()
	if s.mode != 0 {
		if err := f.Chmod(s.mode); err != nil {
			return nil, errors.Join(fmt.Errorf("chmod %s: %w", tmp, err), f.Close())
		}
	}
	return f, nil
}

// scratchFile is a scratch file of the external sort, in the output directory.
type scratchFile struct {
	*os.File
	s *stage
}

// newScratch creates a scratch file for the sort of a table without a key. It
// has the mode of the output files, so it is never more open than the data it
// holds will be once written out. extsort removes it as soon as its run is
// merged; a failed or interrupted run removes what is left in discard.
func (s *stage) newScratch() (extsort.File, error) {
	var name string
	f, err := s.openTemp("sort-", os.O_RDWR, func(tmp string) {
		if s.scratch == nil {
			s.scratch = map[string]*scratchFile{}
		}
		s.scratch[tmp] = nil // set below; nil if the chmod fails, so discard still unlinks it
		name = tmp
	})
	if err != nil {
		return nil, err
	}
	sf := &scratchFile{f, s}
	s.mu.Lock()
	s.scratch[name] = sf
	s.mu.Unlock()
	return sf, nil
}

// Remove closes and deletes the file. It may be called again.
func (f *scratchFile) Remove() error {
	f.s.mu.Lock()
	_, live := f.s.scratch[f.Name()]
	delete(f.s.scratch, f.Name())
	f.s.mu.Unlock()
	if !live {
		return nil
	}
	err := f.Close()
	if rerr := os.Remove(f.Name()); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		err = errors.Join(err, rerr)
	}
	return err
}

// setFinal changes the path the file is renamed to.
func (s *stage) setFinal(idx int, final string) {
	s.mu.Lock()
	s.files[idx].final = final
	s.mu.Unlock()
}

func (t *tempFile) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if err != nil {
		err = fmt.Errorf("write %s: %w", t.label, err)
	}
	return n, err
}

// Close syncs and closes the file, joining the error of the write that led to
// it, if any (see finish).
func (t *tempFile) Close() error { return t.finish(nil) }

// finish syncs and closes the file. werr is the error of writing it.
func (t *tempFile) finish(werr error) error {
	err := werr // already worded by the caller
	// Sync before the rename can become durable, or a crash could leave an
	// empty file in place of the previous dump.
	if err == nil {
		if err = t.f.Sync(); err != nil {
			err = fmt.Errorf("write %s: %w", t.label, err)
		}
	}
	if cerr := t.f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("write %s: %w", t.label, cerr)
	}
	return err
}

// commit renames every temporary file to its final path, replacing the file
// of the previous dump atomically. The renames are the only step after which
// a failure can leave a mix of old and new files.
func (s *stage) commit() error {
	for i := range s.files {
		f := &s.files[i]
		if err := os.Rename(f.tmp, f.final); err != nil {
			return err
		}
		f.renamed = true
	}
	// Make the renames durable.
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// discard removes the temporary files that were not renamed.
func (s *stage) discard() error {
	var errs []error
	s.mu.Lock()
	left := s.scratch
	s.scratch = nil
	s.mu.Unlock()
	for tmp, f := range left {
		// Close the handle too: a later Remove finds nothing registered and
		// would not.
		if f != nil {
			_ = f.Close()
		}
		if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	for _, f := range s.files {
		if f.renamed {
			continue
		}
		if err := os.Remove(f.tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fileWriter wraps the writer of an output file, given the file's final path;
// tests replace it to inject write failures.
var fileWriter = func(path string, f *os.File) io.Writer { return f }
