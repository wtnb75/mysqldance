//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package dump

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive flock on f without blocking. flock locks belong
// to the open file description, so a second open of the file in the same
// process is refused too.
func lockFile(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK):
			return errLockHeld
		default:
			return err
		}
	}
}
