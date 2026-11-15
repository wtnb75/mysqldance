package dump

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// lockFileName is the file in the output directory that holds the lock. It is
// not *.sql, so docker-entrypoint-initdb.d ignores it, and not a temporary
// name, so sweepTemps and removeStale leave it alone. It is never deleted: a
// delete would race with a run that has just opened it, and the next run opens
// the leftover file and locks it again.
const lockFileName = ".mysqldance.lock"

// errOutDirBusy means another run holds the lock of the output directory.
var errOutDirBusy = errors.New("output directory is in use by another mysqldance run; wait for it to finish")

// errLockHeld is what lockFile returns when another holder has the lock.
var errLockHeld = errors.New("lock is held")

// lockOutDir takes the exclusive lock of dir without waiting. It fails with
// errOutDirBusy if another run (in this process or another) holds it. The
// lock is an advisory lock the OS drops when the process ends, so a killed run
// leaves nothing that blocks the next one. The returned function releases it
// and does not remove the file.
func lockOutDir(dir string) (unlock func() error, err error) {
	path := filepath.Join(dir, lockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", dir, err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockHeld) {
			return nil, fmt.Errorf("%s: %w", dir, errOutDirBusy)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// Closing the file drops the lock on every platform.
	return f.Close, nil
}
