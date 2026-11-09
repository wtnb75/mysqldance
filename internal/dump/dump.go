// Package dump writes the dump files of one or more databases into a flat
// output directory.
package dump

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mysqldance/internal/datafile"
	"mysqldance/internal/naming"
	"mysqldance/internal/schemafile"
	"mysqldance/internal/source"
)

// maxFileNameBytes is the common file-name length limit of file systems.
const maxFileNameBytes = 255

// Options configures a dump run.
type Options struct {
	Source source.Config
	DBs    []string
	OutDir string
	Data   datafile.Options
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
	if err := os.MkdirAll(opt.OutDir, 0o755); err != nil {
		return err
	}
	if err := sweepTemps(opt.OutDir); err != nil {
		return err
	}
	st := &stage{dir: opt.OutDir}
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

func dumpDB(ctx context.Context, opt Options, snap *source.Snapshot, db string, seen map[string]string, st *stage) error {
	createDB, err := snap.CreateDatabase(ctx)
	if err != nil {
		return err
	}
	tables, err := snap.Tables(ctx)
	if err != nil {
		return err
	}
	for _, w := range snap.Warnings() {
		_, _ = fmt.Fprintf(opt.Log, "warning: database %q: %s\n", db, w)
	}
	views, deps, err := snap.Views(ctx)
	if err != nil {
		return err
	}

	// Validate every file name before touching the directory.
	schemaName := naming.SchemaFile(db)
	if err := claim(seen, schemaName, fmt.Sprintf("the schema of database %q", db)); err != nil {
		return err
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

	schemaTables := make([]schemafile.Table, len(tables))
	for i, t := range tables {
		schemaTables[i] = schemafile.Table{Name: t.Name, Create: schemafile.StripAutoIncrement(t.Create)}
	}
	byName := map[string]schemafile.View{}
	names := make([]string, len(views))
	for i, v := range views {
		byName[v.Name] = v
		names[i] = v.Name
	}
	ordered := make([]schemafile.View, 0, len(views))
	for _, n := range schemafile.OrderViews(names, deps) {
		v := byName[n]
		ordered = append(ordered, schemafile.View{Name: v.Name, Create: schemafile.StripDefiner(v.Create)})
	}
	if err := st.create(filepath.Join(opt.OutDir, schemaName), func(w io.Writer) error {
		return schemafile.Write(w, db, createDB, schemaTables, ordered)
	}); err != nil {
		return err
	}

	for i, t := range tables {
		rows, err := snap.Rows(ctx, t)
		if err != nil {
			return fmt.Errorf("table %q: %w", t.Name, err)
		}
		cols := make([]string, len(t.Columns))
		for j, c := range t.Columns {
			cols[j] = c.Name
		}
		if err := st.create(filepath.Join(opt.OutDir, dataNames[i]), func(w io.Writer) error {
			return datafile.Write(w, db, t.Name, cols, t.ListColumns, rows, opt.Data)
		}); err != nil {
			return err
		}
	}
	return nil
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
	dir   string
	files []stagedFile
}

type stagedFile struct {
	tmp, final string
	renamed    bool
}

// create writes a new temporary file with write, destined for the final path.
// The temporary file must not exist (O_EXCL never follows a symlink).
// Permissions follow the umask.
func (s *stage) create(final string, write func(io.Writer) error) error {
	tmp := filepath.Join(s.dir, fmt.Sprintf("%s%04d%s", tmpPrefix, len(s.files)+1, tmpSuffix))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	// Recorded before writing, so a partial file is removed by discard.
	s.files = append(s.files, stagedFile{tmp: tmp, final: final})
	err = write(fileWriter(final, f))
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
