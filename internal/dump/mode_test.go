package dump

import (
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
