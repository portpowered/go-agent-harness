//go:build windows

package chatgptauth

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockedBytes is the byte range locked: one byte at offset zero.
const lockedBytes = 1

// tryLockFile takes an exclusive LockFileEx lock on file without blocking.
// Windows releases it when the holding handle is closed or the process exits.
func tryLockFile(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockedBytes, 0, &overlapped)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, lockedBytes, 0, &overlapped)
}
