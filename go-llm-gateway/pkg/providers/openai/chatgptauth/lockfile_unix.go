//go:build unix

package chatgptauth

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes an exclusive flock on file without blocking. flock locks
// belong to the open file description and are released by the kernel when
// the holder exits.
func tryLockFile(file *os.File) (bool, error) {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK):
			return false, nil
		default:
			return false, err
		}
	}
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
