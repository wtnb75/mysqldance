package dump

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockOutDirRefusesSecondHolder(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockOutDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A leftover temporary file must survive the refused attempt: the loser
	// may not touch anything in the directory.
	tmp := filepath.Join(dir, ".mysqldance-0001.tmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = lockOutDir(dir)
	if !errors.Is(err, errOutDirBusy) {
		t.Fatalf("second lockOutDir error = %v, want errOutDirBusy", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not name the directory", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("temporary file touched by the refused run: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	// Released: the next run gets the lock, and the file was not deleted.
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Fatalf("lock file removed on unlock: %v", err)
	}
	unlock, err = lockOutDir(dir)
	if err != nil {
		t.Fatalf("lockOutDir after unlock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestLockFileIsNotOutput(t *testing.T) {
	if strings.HasSuffix(lockFileName, ".sql") {
		t.Errorf("lock file name %q ends in .sql", lockFileName)
	}
	if isTempName(lockFileName) {
		t.Errorf("lock file name %q would be swept as a temporary file", lockFileName)
	}
	dir := t.TempDir()
	unlock, err := lockOutDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	if err := sweepTemps(slog.New(slog.DiscardHandler), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Errorf("sweepTemps removed the lock file: %v", err)
	}
}

// TestLockHolderProcess is not a test: it is the child of
// TestLockSurvivesKill. It takes the lock, says so and waits to be killed.
func TestLockHolderProcess(t *testing.T) {
	dir := os.Getenv("MYSQLDANCE_LOCK_TEST_DIR")
	if dir == "" {
		t.Skip("helper process of TestLockSurvivesKill")
	}
	if _, err := lockOutDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatal(err)
	}
	// Until killed; the timeout only keeps an orphan from living on if the
	// parent test dies first.
	time.Sleep(time.Minute)
}

func TestLockSurvivesKill(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestLockHolderProcess$")
	cmd.Env = append(os.Environ(), "MYSQLDANCE_LOCK_TEST_DIR="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper did not take the lock: %q, %v", line, err)
	}
	if _, err := lockOutDir(dir); !errors.Is(err, errOutDirBusy) {
		t.Errorf("lockOutDir while another process holds it = %v, want errOutDirBusy", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// The lock file is still there, but the OS released the lock.
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Fatalf("lock file missing after kill: %v", err)
	}
	unlock, err := lockOutDir(dir)
	if err != nil {
		t.Fatalf("lockOutDir after the holder was killed: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

// TestRunRefusesLockedOutDir needs a server: Run opens the source first.
func TestRunRefusesLockedOutDir(t *testing.T) {
	e := newEnv(t)
	name := uniqueName(t)
	e.createDB(t, name)
	e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1)")
	dir := t.TempDir()
	unlock, err := lockOutDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	tmp := filepath.Join(dir, ".mysqldance-0001.tmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), Options{Source: e.cfg, DBs: []string{name}, OutDir: dir})
	if !errors.Is(err, errOutDirBusy) {
		t.Fatalf("Run on a locked directory = %v, want errOutDirBusy", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("Run swept the directory before it checked the lock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	e.dump(t, dir, name)
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Errorf("lock file removed by a finished run: %v", err)
	}
}
