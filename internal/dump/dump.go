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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
	"mysqldance/internal/writer"
)

// maxFileNameBytes is the common file-name length limit of file systems.
const maxFileNameBytes = 255

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
	// Log receives warnings; nil discards them.
	Log io.Writer
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
		opt.Log = io.Discard
	}
	if opt.Writer == nil {
		opt.Writer = writer.NewMySQL(opt.Data)
	} else if opt.Data != (datafile.Options{}) {
		return errors.New("Options.Data configures the default writer and cannot be combined with Options.Writer")
	}
	if err := prepareOutDir(opt.OutDir, opt.DirMode); err != nil {
		return err
	}
	if err := sweepTemps(opt.OutDir); err != nil {
		return err
	}
	st := &stage{dir: opt.OutDir, mode: opt.FileMode}
	defer func() { err = errors.Join(err, st.discard()) }()
	// Lower-cased output file name -> what it was generated for, across the
	// whole run, so names that differ only in case are rejected.
	seen := map[string]string{}
	// One connection and one transaction for all databases, so they all
	// come from the same point in time.
	snap, err := source.Open(ctx, opt.Source, opt.DBs...)
	if err != nil {
		// Open covers every database (system-database check, lock decision),
		// so the error is not attributed to the first one.
		return err
	}
	defer func() { err = errors.Join(err, snap.Close()) }()
	for i, db := range opt.DBs {
		if i > 0 {
			if err := snap.Use(ctx, db); err != nil {
				return fmt.Errorf("database %q: %w", db, err)
			}
		}
		if err := dumpDB(ctx, opt, snap, db, seen, st); err != nil {
			return fmt.Errorf("database %q: %w", db, err)
		}
	}
	// Everything was read and written. From here on the output directory
	// changes, so stop only for a cancel that arrived before this point.
	if err := ctx.Err(); err != nil {
		return err
	}
	// The dump may have taken long, so check the owned names again before the
	// first rename.
	for _, db := range opt.DBs {
		if _, err := ownedEntries(opt.OutDir, db); err != nil {
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
		if err := removeStale(opt.OutDir, db, written); err != nil {
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

func dumpDB(ctx context.Context, opt Options, snap *source.Snapshot, db string, seen map[string]string, st *stage) error {
	createDB, err := snap.CreateDatabase(ctx)
	if err != nil {
		return err
	}
	tables, warnings, err := snap.Tables(ctx)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		_, _ = fmt.Fprintf(opt.Log, "warning: database %q: %s\n", db, w)
	}
	views, deps, err := snap.Views(ctx)
	if err != nil {
		return err
	}
	var defs map[string]source.TableDef
	if opt.Writer.NeedsDefs() {
		if defs, err = snap.Defs(ctx); err != nil {
			return err
		}
		for i := range tables {
			if d, ok := defs[tables[i].Name]; ok {
				tables[i].Def = &d
			}
		}
	}
	schema := writer.Schema{
		DB: db, CreateDatabase: createDB, Tables: tables, Views: views, ViewDeps: deps, Defs: defs,
		Warn: func(msg string) { _, _ = fmt.Fprintf(opt.Log, "warning: database %q: %s\n", db, msg) },
	}

	// Validate every file name before touching the directory.
	schemaName := naming.SchemaFile(db)
	if err := claim(seen, schemaName, fmt.Sprintf("the schema of database %q", db)); err != nil {
		return err
	}
	cw, hasConstraints := opt.Writer.(writer.ConstraintWriter)
	if hasConstraints {
		if err := claim(seen, naming.ConstraintsFile(db), fmt.Sprintf("the constraints of database %q", db)); err != nil {
			return err
		}
	}
	dataNames := make([]string, len(tables))
	for i, t := range tables {
		dataNames[i] = naming.DataFile(db, t.Name)
		if err := claim(seen, dataNames[i], fmt.Sprintf("table %q of database %q", t.Name, db)); err != nil {
			return err
		}
	}

	// Refuse owned entries that are not regular files before writing anything.
	if _, err := ownedEntries(opt.OutDir, db); err != nil {
		return err
	}

	if err := st.create(filepath.Join(opt.OutDir, schemaName), func(w io.Writer) error {
		return opt.Writer.WriteSchema(w, schema)
	}); err != nil {
		return err
	}

	// The tables are read and written in parallel, one per reader. Whatever
	// order they finish in, the output is the same: file contents depend on the
	// table alone, and the checks below run in table order.
	partNames := make([][]string, len(tables))
	errs := eachTable(ctx, snap.Readers(), len(tables), func(ctx context.Context, r *source.Snapshot, i int) error {
		var err error
		partNames[i], err = writeTable(ctx, opt, r, db, tables[i], st)
		return err
	})
	if err := firstError(ctx, errs); err != nil {
		return err
	}
	// A cancel before the first table started leaves no error behind.
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, t := range tables {
		if len(partNames[i]) <= 1 {
			continue
		}
		// The part names are known only now. Claim them like the others,
		// before anything is committed.
		for _, n := range partNames[i] {
			if err := claim(seen, n, fmt.Sprintf("table %q of database %q", t.Name, db)); err != nil {
				return err
			}
		}
	}
	if hasConstraints {
		return st.create(filepath.Join(opt.OutDir, naming.ConstraintsFile(db)), func(w io.Writer) error {
			return cw.WriteConstraints(w, schema)
		})
	}
	return nil
}

// writeTable reads the rows of t through r, renders them and stages the data
// files, and returns the final names of the files (without the directory).
func writeTable(ctx context.Context, opt Options, r *source.Snapshot, db string, t source.TableInfo, st *stage) ([]string, error) {
	rows, err := r.Rows(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("table %q: %w", t.Name, err)
	}
	parts, err := dataParts(opt.Writer, db, t, rows)
	if err != nil {
		return nil, err
	}
	names := naming.DataFileParts(db, t.Name, len(parts))
	for g, part := range parts {
		if err := st.create(filepath.Join(opt.OutDir, names[g]), part); err != nil {
			return nil, err
		}
	}
	return names, nil
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

// dataParts asks w for the data files of t. A writer that returns none would
// leave the table without a file and no error, so that is refused.
func dataParts(w writer.Writer, db string, t source.TableInfo, rows [][]sqlvalue.Value) ([]writer.Part, error) {
	parts := w.DataParts(db, t, rows)
	if len(parts) == 0 {
		return nil, fmt.Errorf("table %q: the writer produced no data file", t.Name)
	}
	return parts, nil
}

// claim checks the length of an output file name and records it in seen,
// failing if another output file has the same name ignoring case.
func claim(seen map[string]string, name, what string) error {
	if len(name) > maxFileNameBytes {
		return fmt.Errorf("output file name for %s is %d bytes, over the %d-byte limit: %s",
			what, len(name), maxFileNameBytes, name)
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

// ownedEntries returns the names of the entries in dir that belong to db's
// dump. An entry with an owned name that is not a regular file (a symlink, a
// directory, ...) is an error.
func ownedEntries(dir, db string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var owned []string
	for _, e := range entries {
		if !naming.IsOwnedBy(e.Name(), db) {
			continue
		}
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("refusing to replace %s: not a regular file", filepath.Join(dir, e.Name()))
		}
		owned = append(owned, e.Name())
	}
	return owned, nil
}

// removeStale deletes the files of db's earlier dump that this run did not
// write again.
func removeStale(dir, db string, written map[string]bool) error {
	owned, err := ownedEntries(dir, db)
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

// sweepTemps removes the temporary files an earlier run left behind.
func sweepTemps(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, tmpPrefix) && strings.HasSuffix(n, tmpSuffix) {
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
}

type stagedFile struct {
	tmp, final string
	renamed    bool
}

// create writes a new temporary file with write, destined for the final path.
// The temporary file must not exist (O_EXCL never follows a symlink).
// Permissions follow the umask unless s.mode is set, which is applied exactly.
func (s *stage) create(final string, write func(io.Writer) error) error {
	// The number in the temporary name only has to be unique; which table gets
	// which number does not matter, since the files are renamed to their final
	// names.
	s.mu.Lock()
	s.n++
	tmp := filepath.Join(s.dir, fmt.Sprintf("%s%04d%s", tmpPrefix, s.n, tmpSuffix))
	s.mu.Unlock()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, cmp.Or(s.mode, 0o666))
	if err != nil {
		return err
	}
	// Recorded before writing, so a partial file is removed by discard.
	s.mu.Lock()
	s.files = append(s.files, stagedFile{tmp: tmp, final: final})
	s.mu.Unlock()
	if s.mode != 0 {
		// The umask filtered the mode given to OpenFile.
		err = f.Chmod(s.mode)
	}
	if err == nil {
		err = write(fileWriter(final, f))
	}
	// Sync before the rename can become durable, or a crash could leave an
	// empty file in place of the previous dump.
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("write %s: %w", final, err)
	}
	return nil
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
