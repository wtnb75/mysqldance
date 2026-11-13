package dump

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestStageCreateFileMode(t *testing.T) {
	dir := t.TempDir()
	// 0o664 differs from the default 0o666 &^ umask(022) = 0o644, so this
	// fails unless the mode is applied exactly.
	s := &stage{dir: dir, mode: 0o664}
	if err := s.create(filepath.Join(dir, "a.sql"), func(w io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, s.files[0].tmp); got != 0o664 {
		t.Errorf("file mode = %o, want 664", got)
	}
}

func TestStageScratchFile(t *testing.T) {
	dir := t.TempDir()
	// Not the default 0o644, so this fails unless the output files' mode is used.
	s := &stage{dir: dir, mode: 0o664}
	f, err := s.newScratch()
	if err != nil {
		t.Fatal(err)
	}
	sf, ok := f.(*scratchFile)
	if !ok {
		t.Fatalf("scratch file is a %T", f)
	}
	if got := modeOf(t, sf.Name()); got != 0o664 {
		t.Errorf("scratch file mode = %o, want 664 (the output file mode)", got)
	}
	if filepath.Dir(sf.Name()) != dir {
		t.Errorf("scratch file %s is not in the output directory", sf.Name())
	}
	// It is named so that the sweep of a killed run finds it.
	if !isTempName(filepath.Base(sf.Name())) {
		t.Errorf("scratch file name %q is not a temporary name", filepath.Base(sf.Name()))
	}
	if _, err := f.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(f); err != nil || string(b) != "abc" {
		t.Fatalf("read back %q, %v", b, err)
	}
	if len(s.files) != 0 {
		t.Errorf("a scratch file is among the files to commit: %v", s.files)
	}
}

func TestStageScratchRemoveAndDiscard(t *testing.T) {
	dir := t.TempDir()
	s := &stage{dir: dir}
	a, err := s.newScratch()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.newScratch()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("%d files after removing one scratch file, want 1", len(entries))
	}
	// discard (a failed or interrupted run) removes the one still there.
	if err := s.discard(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%d files left after discard", len(entries))
	}
	_ = b.Remove() // already gone: no error
}

func TestStageDiscardClosesScratchFiles(t *testing.T) {
	s := &stage{dir: t.TempDir()}
	f, err := s.newScratch()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.discard(); err != nil {
		t.Fatal(err)
	}
	// discard closed the handle, not only unlinked the path.
	if _, err := f.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("write after discard: %v, want os.ErrClosed", err)
	}
	if err := f.Remove(); err != nil {
		t.Errorf("Remove after discard: %v", err)
	}
}

func TestSortMemoryOrDefault(t *testing.T) {
	for _, tt := range []struct{ in, want int64 }{
		{0, DefaultSortMemory}, {-1, DefaultSortMemory}, {4096, 4096},
	} {
		if got := sortMemoryOrDefault(tt.in); got != tt.want {
			t.Errorf("sortMemoryOrDefault(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestSweepTempsRemovesScratchFiles(t *testing.T) {
	dir := t.TempDir()
	s := &stage{dir: dir}
	if _, err := s.newScratch(); err != nil { // left behind by a killed run
		t.Fatal(err)
	}
	if err := sweepTemps(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%d files left after the sweep", len(entries))
	}
}

func TestPrepareOutDirNew(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "parent", "out")
	if err := prepareOutDir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("created dir mode = %o, want 700", got)
	}
	// Parents are not the output directory itself, so they keep the default.
	if got := modeOf(t, filepath.Join(root, "parent")); got == 0o700 {
		t.Errorf("parent mode = %o, must not follow --dir-mode", got)
	}
}

func TestPrepareOutDirExistingKeepsMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := prepareOutDir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o750 {
		t.Errorf("existing dir mode = %o, want 750 (unchanged)", got)
	}
}

func TestPrepareOutDirTrailingSlash(t *testing.T) {
	root := t.TempDir()
	if err := prepareOutDir(filepath.Join(root, "p", "out")+string(filepath.Separator), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, filepath.Join(root, "p", "out")); got != 0o700 {
		t.Errorf("created dir mode = %o, want 700", got)
	}
}

func TestPrepareOutDirNotADirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareOutDir(path, 0); err == nil {
		t.Error("prepareOutDir on a regular file succeeded, want an error")
	}
}

func TestPrepareOutDirDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if err := prepareOutDir(dir, 0); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got&0o700 != 0o700 {
		t.Errorf("default dir mode = %o, owner must have rwx", got)
	}
}
