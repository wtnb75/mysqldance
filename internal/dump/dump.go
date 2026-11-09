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
// removes the files the database owned from an earlier dump, then writes the
// schema file and one data file per table. Other files are left alone.
func Run(ctx context.Context, opt Options) error {
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
	// Lower-cased output file name -> what it was generated for, across the
	// whole run, so names that differ only in case are rejected.
	seen := map[string]string{}
	for _, db := range opt.DBs {
		if err := dumpDB(ctx, opt, db, seen); err != nil {
			return fmt.Errorf("database %q: %w", db, err)
		}
	}
	return nil
}

func dumpDB(ctx context.Context, opt Options, db string, seen map[string]string) (err error) {
	snap, err := source.Open(ctx, opt.Source, db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, snap.Close()) }()

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

	if err := removeOwned(opt.OutDir, db); err != nil {
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
	if err := writeFile(filepath.Join(opt.OutDir, schemaName), func(w io.Writer) error {
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
		if err := writeFile(filepath.Join(opt.OutDir, dataNames[i]), func(w io.Writer) error {
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

// removeOwned deletes the regular files in dir that belong to db's dump.
func removeOwned(dir, db string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !naming.IsOwnedBy(e.Name(), db) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}
